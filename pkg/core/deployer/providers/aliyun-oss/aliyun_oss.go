package aliyunoss

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	"github.com/samber/lo"

	"github.com/certimate-go/certimate/pkg/core"
	cmgrimpl "github.com/certimate-go/certimate/pkg/core/certmgr/providers/aliyun-cas"
	alidns "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/alidns"
	osssdk "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/oss"
	xalibabacloud "github.com/certimate-go/certimate/pkg/utils/third-party/alibabacloud"
)

type (
	Provider     = core.Deployer
	DeployResult = core.DeployerDeployResult
)

type DeployerConfig struct {
	// 阿里云 AccessKeyId。
	AccessKeyId string `json:"accessKeyId"`
	// 阿里云 AccessKeySecret。
	AccessKeySecret string `json:"accessKeySecret"`
	// 阿里云资源组 ID。
	ResourceGroupId string `json:"resourceGroupId,omitempty"`
	// 阿里云地域。
	Region string `json:"region"`
	// 存储桶名。
	Bucket string `json:"bucket"`
	// 自定义域名（不支持泛域名）。
	Domain string `json:"domain"`

	// 是否自动接入域名（默认关闭）。
	// 开启后部署时会自动完成：CnameToken 所有权验证 → 写 TXT 记录 → 等待验证生效 → 绑定证书。
	AutoOnboard bool `json:"autoOnboard,omitempty"`
	// DNS 提供商访问凭证（阿里云 AK），autoOnboard 开启时必填。
	DnsAccessKeyId string `json:"dnsAccessKeyId,omitempty"`
	// DNS 提供商访问凭证（阿里云 SK）。
	DnsAccessKeySecret string `json:"dnsAccessKeySecret,omitempty"`
	// CNAME 冲突时是否覆盖（注意：所有权验证 TXT 记录固定覆盖，不受此开关影响）。
	DnsOverwriteExisting bool `json:"dnsOverwriteExisting,omitempty"`
	// 等待域名所有权验证的超时秒数；<=0 时取默认值 600。
	WaitVerifyTimeout int32 `json:"waitVerifyTimeout,omitempty"`
}

// 非导出接口：OSS Cname 客户端，便于单元测试注入 fake 实现。
type ossCnameClient interface {
	ListBucketCnameWithContext(ctx context.Context, req *osssdk.ListBucketCnameRequest) (*osssdk.ListBucketCnameResponse, error)
	CreateCnameTokenWithContext(ctx context.Context, req *osssdk.CreateCnameTokenRequest) (*osssdk.CreateCnameTokenResponse, error)
	GetCnameTokenWithContext(ctx context.Context, cname string) (*osssdk.GetCnameTokenResponse, error)
	PutBucketCnameWithContext(ctx context.Context, req *osssdk.PutCnameRequest) (*osssdk.PutCnameResponse, error)
}

// 非导出接口：DNS 记录客户端（阿里云解析），便于单元测试注入 fake 实现。
type dnsRecordClient interface {
	EnsureDomainRecord(ctx context.Context, req *alidns.EnsureDomainRecordRequest) (*alidns.EnsureDomainRecordResult, error)
}

type Deployer struct {
	config *DeployerConfig
	logger *slog.Logger

	// OSS Cname 客户端。
	sdkClient ossCnameClient
	// 证书管理器。
	sdkCertmgr core.Certmgr
	// DNS 记录客户端；仅 autoOnboard 开启时初始化。
	dnsClient dnsRecordClient

	// 域名所有权验证的轮询间隔（<=0 时取默认 5 秒；便于单元测试注入更短间隔）
	ownershipVerifyPollInterval time.Duration
}

var _ Provider = (*Deployer)(nil)

func NewDeployer(config *DeployerConfig) (*Deployer, error) {
	if config == nil {
		return nil, fmt.Errorf("the configuration of the deployer provider is nil")
	}

	client, err := createSDKClient(config.AccessKeyId, config.AccessKeySecret, config.Region, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("could not create client: %w", err)
	}

	pcertmgr, err := cmgrimpl.NewCertmgr(&cmgrimpl.CertmgrConfig{
		AccessKeyId:     config.AccessKeyId,
		AccessKeySecret: config.AccessKeySecret,
		ResourceGroupId: config.ResourceGroupId,
		Region:          lo.Ternary(xalibabacloud.IsIntlRegion(config.Region), "ap-southeast-1", ""),
	})
	if err != nil {
		return nil, fmt.Errorf("could not create certmgr: %w", err)
	}

	var pdnsclient dnsRecordClient
	if config.AutoOnboard {
		if config.DnsAccessKeyId == "" || config.DnsAccessKeySecret == "" {
			return nil, fmt.Errorf("config `dnsAccessKeyId` and `dnsAccessKeySecret` are required when auto onboard enabled")
		}

		dnsclient, err := alidns.NewClient(config.DnsAccessKeyId, config.DnsAccessKeySecret)
		if err != nil {
			return nil, fmt.Errorf("could not create dns client: %w", err)
		}

		pdnsclient = dnsclient
	}

	return &Deployer{
		config:     config,
		logger:     slog.Default(),
		sdkClient:  client,
		sdkCertmgr: pcertmgr,
		dnsClient:  pdnsclient,
	}, nil
}

func (d *Deployer) SetLogger(logger *slog.Logger) {
	if logger == nil {
		d.logger = slog.New(slog.DiscardHandler)
	} else {
		d.logger = logger
	}

	d.sdkCertmgr.SetLogger(logger)
}

func (d *Deployer) Deploy(ctx context.Context, certPEM, privkeyPEM string) (*DeployResult, error) {
	if d.config.Bucket == "" {
		return nil, fmt.Errorf("config `bucket` is required")
	}
	if d.config.Domain == "" {
		return nil, fmt.Errorf("config `domain` is required")
	}

	// 开启自动接入域名时，泛域名无法通过 CnameToken 完成所有权验证，须明确拒绝
	if d.config.AutoOnboard && strings.HasPrefix(d.config.Domain, "*") {
		return nil, fmt.Errorf("config `domain` must not be a wildcard domain when auto onboard enabled")
	}

	// 开启自动接入域名时，先完成域名接入（所有权验证），再上传并绑定证书；否则走原路径
	if d.config.AutoOnboard {
		return d.deployWithAutoOnboard(ctx, certPEM, privkeyPEM)
	}

	// 上传证书
	upres, err := d.sdkCertmgr.Upload(ctx, certPEM, privkeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to upload certificate file: %w", err)
	} else {
		d.logger.Info("ssl certificate uploaded", slog.Any("result", upres))
	}

	// 为存储空间绑定自定义域名
	// REF: https://help.aliyun.com/zh/oss/developer-reference/putcname
	putBucketCnameReq := &osssdk.PutCnameRequest{
		Cname: &osssdk.PutCnameRequestCname{
			Domain: tea.String(d.config.Domain),
			CertificateConfiguration: &osssdk.PutCnameRequestCnameCertificateConfiguration{
				CertId:      tea.String(upres.ExtendedData["CertIdWithRegion"].(string)),
				Certificate: tea.String(certPEM),
				PrivateKey:  tea.String(privkeyPEM),
				Force:       tea.Bool(true),
			},
		},
	}
	putBucketCnameResp, err := d.sdkClient.PutBucketCnameWithContext(ctx, putBucketCnameReq)
	d.logger.Debug("sdk request 'oss.PutBucketCname'", slog.String("params.bucket", d.config.Bucket), slog.Any("request", putBucketCnameReq), slog.Any("response", putBucketCnameResp))
	if err != nil {
		return nil, fmt.Errorf("failed to execute sdk request 'oss.PutBucketCname': %w", err)
	}

	return &DeployResult{}, nil
}

func (d *Deployer) deployWithAutoOnboard(ctx context.Context, certPEM, privkeyPEM string) (*DeployResult, error) {
	// 上传证书
	upres, err := d.sdkCertmgr.Upload(ctx, certPEM, privkeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to upload certificate file: %w", err)
	} else {
		d.logger.Info("ssl certificate uploaded", slog.Any("result", upres))
	}

	// 查询自定义域名是否已接入存储空间
	// REF: https://help.aliyun.com/zh/oss/developer-reference/getcname
	listBucketCnameResp, err := d.sdkClient.ListBucketCnameWithContext(ctx, &osssdk.ListBucketCnameRequest{})
	d.logger.Debug("sdk request 'oss.ListBucketCname'", slog.String("params.bucket", d.config.Bucket), slog.Any("response", listBucketCnameResp))
	if err != nil {
		// 404（NoSuchCname）表示该 Bucket 从未绑定过任何 Cname，属正常空态（如全新 Bucket 接入首个域名），
		// 视为「未接入任何域名」继续 CnameToken 流程；其余错误原样上抛
		if !osssdk.IsNoCnameError(err) {
			return nil, fmt.Errorf("failed to execute sdk request 'oss.ListBucketCname': %w", err)
		}
		d.logger.Info("no cname found on bucket, treat as not onboarded", slog.String("params.bucket", d.config.Bucket))
		listBucketCnameResp = &osssdk.ListBucketCnameResponse{}
	}

	onboarded := lo.ContainsBy(listBucketCnameResp.Cnames, func(item osssdk.ListBucketCnameResponseCname) bool {
		return item.Domain != nil && tea.StringValue(item.Domain) == d.config.Domain
	})

	// 已接入：直接绑定证书即可
	if onboarded {
		d.logger.Info("custom domain already onboarded", slog.String("domain", d.config.Domain))
		if err := d.bindCnameWithCertificate(ctx, certPEM, privkeyPEM, upres); err != nil {
			return nil, err
		}
		return &DeployResult{}, nil
	}

	// 未接入：创建 CnameToken 用于域名所有权验证
	// REF: https://help.aliyun.com/zh/oss/developer-reference/createcnametoken
	// 注意：CnameToken 属敏感凭据，日志中仅记录是否获取成功，不得记录响应原文（节点日志会持久化落库）
	createCnameTokenResp, err := d.sdkClient.CreateCnameTokenWithContext(ctx, &osssdk.CreateCnameTokenRequest{
		Cname: &osssdk.CreateCnameTokenRequestCname{
			Domain: tea.String(d.config.Domain),
		},
	})
	d.logger.Debug("sdk request 'oss.CreateCnameToken'",
		slog.String("params.bucket", d.config.Bucket),
		slog.String("params.domain", d.config.Domain),
		slog.Bool("tokenObtained", err == nil && createCnameTokenResp != nil && tea.StringValue(createCnameTokenResp.Token) != ""),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sdk request 'oss.CreateCnameToken': %w", err)
	}

	token := tea.StringValue(createCnameTokenResp.Token)
	if token == "" {
		return nil, fmt.Errorf("failed to execute sdk request 'oss.CreateCnameToken': token is empty")
	}

	// 写入域名所有权验证所需的 TXT 记录（记录值为 CnameToken）
	// REF: https://help.aliyun.com/zh/oss/developer-reference/putcname（NeedVerifyDomainOwnership 错误的处理步骤）
	// 注意：token 为一次性校验值，残留旧值必然阻塞验证，因此此处固定覆盖已有记录
	mainDomain, subDomain, err := alidns.SplitMainDomain(d.config.Domain)
	if err != nil {
		return nil, fmt.Errorf("failed to split domain: %w", err)
	}

	txtSubDomain := lo.Ternary(subDomain == "", "_dnsauth", "_dnsauth."+subDomain)
	ensureDomainRecordResp, err := d.dnsClient.EnsureDomainRecord(ctx, &alidns.EnsureDomainRecordRequest{
		MainDomain:        mainDomain,
		SubDomain:         txtSubDomain,
		RecordType:        "TXT",
		RecordValue:       token,
		OverwriteExisting: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure dns record: %w", err)
	}
	d.logger.Info("dns record ensured",
		slog.String("recordType", "TXT"),
		slog.String("mainDomain", mainDomain),
		slog.String("subDomain", txtSubDomain),
		slog.Any("result", ensureDomainRecordResp))

	// 轮询等待域名所有权验证完成
	// 注意：官方 GetCnameToken 响应仅含 Bucket/Cname/Token/ExpireTime，并无校验状态字段，
	//       验证是否完成的唯一可靠信号是 PutCname 不再返回 403 NeedVerifyDomainOwnership，
	//       因此轮询期间以 PutCname 探测（该方法同时承担最终绑定证书的职责）。
	waitVerifyTimeout := d.config.WaitVerifyTimeout
	if waitVerifyTimeout <= 0 {
		waitVerifyTimeout = 600
	}
	deadline := time.Now().Add(time.Duration(waitVerifyTimeout) * time.Second)

	pollInterval := d.ownershipVerifyPollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}

	for {
		// 注意：同上，日志中仅记录 Token 是否已返回，不得记录响应原文
		getCnameTokenResp, err := d.sdkClient.GetCnameTokenWithContext(ctx, d.config.Domain)
		d.logger.Debug("sdk request 'oss.GetCnameToken'",
			slog.String("params.bucket", d.config.Bucket),
			slog.String("params.domain", d.config.Domain),
			slog.Bool("tokenObtained", err == nil && getCnameTokenResp != nil && tea.StringValue(getCnameTokenResp.Token) != ""),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to execute sdk request 'oss.GetCnameToken': %w", err)
		}

		err = d.bindCnameWithCertificate(ctx, certPEM, privkeyPEM, upres)
		if err == nil {
			return &DeployResult{}, nil
		} else if !strings.Contains(err.Error(), "NeedVerifyDomainOwnership") {
			// 非验证未完成类错误（如域名未备案等），直接上抛
			return nil, err
		} else if time.Now().Add(pollInterval).After(deadline) {
			return nil, fmt.Errorf("the domain ownership verification is not completed within %d seconds, last error: %w", waitVerifyTimeout, err)
		}

		d.logger.Info("domain ownership verification in progress", slog.String("domain", d.config.Domain))

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// 为存储空间绑定自定义域名并配置证书（Force=true，允许覆盖已有证书配置）。
// 返回的错误已包含平台原始信息。
func (d *Deployer) bindCnameWithCertificate(ctx context.Context, certPEM, privkeyPEM string, upres *core.CertmgrUploadResult) error {
	// 为存储空间绑定自定义域名
	// REF: https://help.aliyun.com/zh/oss/developer-reference/putcname
	putBucketCnameReq := &osssdk.PutCnameRequest{
		Cname: &osssdk.PutCnameRequestCname{
			Domain: tea.String(d.config.Domain),
			CertificateConfiguration: &osssdk.PutCnameRequestCnameCertificateConfiguration{
				CertId:      tea.String(upres.ExtendedData["CertIdWithRegion"].(string)),
				Certificate: tea.String(certPEM),
				PrivateKey:  tea.String(privkeyPEM),
				Force:       tea.Bool(true),
			},
		},
	}
	putBucketCnameResp, err := d.sdkClient.PutBucketCnameWithContext(ctx, putBucketCnameReq)
	d.logger.Debug("sdk request 'oss.PutBucketCname'", slog.String("params.bucket", d.config.Bucket), slog.String("params.domain", d.config.Domain), slog.Any("response", putBucketCnameResp))
	if err != nil {
		return fmt.Errorf("failed to execute sdk request 'oss.PutBucketCname': %w", err)
	}

	return nil
}

func createSDKClient(accessKeyId, accessKeySecret, region, bucket string) (*osssdk.Client, error) {
	client, err := osssdk.NewClient("",
		osssdk.WithAkSk(accessKeyId, accessKeySecret),
		osssdk.WithRegion(region),
		osssdk.WithBucket(bucket),
	)
	if err != nil {
		return nil, err
	}

	return client, nil
}
