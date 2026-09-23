package qiniukodo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qiniu/go-sdk/v7/auth"

	"github.com/certimate-go/certimate/pkg/core"
	cmgrimpl "github.com/certimate-go/certimate/pkg/core/certmgr/providers/qiniu-sslcert"
	alidnssdk "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/alidns"
	qiniusdk "github.com/certimate-go/certimate/pkg/sdk3rd/qiniu"
)

type (
	Provider     = core.Deployer
	DeployResult = core.DeployerDeployResult
)

// 七牛域名操作状态（REF: https://developer.qiniu.com/fusion/4246/the-domain-name）。
const (
	domainOperatingStateSuccess  = "success"  // 已生效（最近一次操作成功）
	domainOperatingStateFailed   = "failed"   // 已失败（如备案审核未通过）
	domainOperatingStateFrozen   = "frozen"   // 已冻结
	domainOperatingStateOfflined = "offlined" // 已下线
)

type DeployerConfig struct {
	// 七牛云 AccessKey。
	AccessKey string `json:"accessKey"`
	// 七牛云 SecretKey。
	SecretKey string `json:"secretKey"`
	// 存储桶名。暂时无用。
	Bucket string `json:"bucket"`
	// 自定义域名（不支持泛域名）。
	Domain string `json:"domain"`

	// 是否自动接入域名（默认关闭）；关闭时部署行为与原有逻辑完全一致。
	AutoOnboard bool `json:"autoOnboard,omitempty"`
	// DNS 提供商访问凭证（阿里云 AK），自动接入域名开启时必填。
	DnsAccessKeyId string `json:"dnsAccessKeyId,omitempty"`
	// DNS 提供商访问凭证（阿里云 SK）。
	DnsAccessKeySecret string `json:"dnsAccessKeySecret,omitempty"`
	// CNAME 已存在且指向不同时是否覆盖更新；关闭时冲突将报错（错误信息含现有记录值）。
	DnsOverwriteExisting bool `json:"dnsOverwriteExisting,omitempty"`
	// 等待域名校验生效的超时秒数；小于等于 0 时取默认值 600。
	WaitVerifyTimeout int32 `json:"waitVerifyTimeout,omitempty"`
}

// 七牛域名管理客户端（测试缝）。
type qiniuDomainClient interface {
	GetDomainInfo(ctx context.Context, domain string) (*qiniusdk.GetDomainInfoResponse, error)
	CreateDomain(ctx context.Context, req *qiniusdk.CreateDomainRequest) (*qiniusdk.CreateDomainResponse, error)
}

// DNS 解析记录客户端（测试缝）。
type dnsRecordClient interface {
	EnsureDomainRecord(ctx context.Context, req *alidnssdk.EnsureDomainRecordRequest) (*alidnssdk.EnsureDomainRecordResult, error)
}

// 七牛 Kodo 管理客户端（测试缝）。
type qiniuKodoClient interface {
	BindBucketCert(ctx context.Context, domain string, certId string) (*qiniusdk.BindBucketCertResponse, error)
}

const (
	// 默认等待域名校验生效的超时时间。
	defaultWaitVerifyTimeout = 600 * time.Second
	// 轮询域名状态的间隔时间。
	defaultPollInterval = 5 * time.Second
)

type Deployer struct {
	config     *DeployerConfig
	logger     *slog.Logger
	sdkClient  qiniuKodoClient
	sdkCertmgr core.Certmgr

	sdkDomain    qiniuDomainClient // 自动接入域名时使用的七牛域名管理客户端
	sdkDNS       dnsRecordClient   // 自动接入域名时使用的 DNS 解析记录客户端
	pollInterval time.Duration     // 轮询间隔（测试可注入更小值）
}

var _ Provider = (*Deployer)(nil)

func NewDeployer(config *DeployerConfig) (*Deployer, error) {
	if config == nil {
		return nil, fmt.Errorf("the configuration of the deployer provider is nil")
	}

	client := qiniusdk.NewKodoManager(auth.New(config.AccessKey, config.SecretKey))

	pcertmgr, err := cmgrimpl.NewCertmgr(&cmgrimpl.CertmgrConfig{
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("could not create certmgr: %w", err)
	}

	deployer := &Deployer{
		config:       config,
		logger:       slog.Default(),
		sdkClient:    client,
		sdkCertmgr:   pcertmgr,
		pollInterval: defaultPollInterval,
	}

	// 自动接入域名时，需额外装配七牛域名管理客户端与 DNS 解析记录客户端
	if config.AutoOnboard {
		if config.Bucket == "" {
			return nil, fmt.Errorf("config `bucket` is required when auto onboard enabled")
		}
		if config.DnsAccessKeyId == "" || config.DnsAccessKeySecret == "" {
			return nil, fmt.Errorf("config `dnsAccessKeyId` and `dnsAccessKeySecret` are required when auto onboard enabled")
		}

		deployer.sdkDomain = qiniusdk.NewCdnManager(auth.New(config.AccessKey, config.SecretKey))

		dnsClient, err := alidnssdk.NewClient(config.DnsAccessKeyId, config.DnsAccessKeySecret)
		if err != nil {
			return nil, fmt.Errorf("could not create dns client: %w", err)
		}
		deployer.sdkDNS = dnsClient
	}

	return deployer, nil
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
	if d.config.Domain == "" {
		return nil, fmt.Errorf("config `domain` is required")
	}

	// 自动接入域名（autoOnboard 关闭时不执行任何额外操作，保持原有部署路径）
	if d.config.AutoOnboard {
		// 自动接入依赖按主机记录写入 DNS 解析，泛域名无法拆分主机记录，明确拒绝；
		// 该防线仅约束 autoOnboard 开启路径，关闭时保持原有行为
		if strings.HasPrefix(d.config.Domain, "*") {
			return nil, fmt.Errorf("config `domain` must not be a wildcard domain when auto onboard enabled")
		}

		if err := d.autoOnboardDomain(ctx); err != nil {
			return nil, err
		}
	}

	// 上传证书
	upres, err := d.sdkCertmgr.Upload(ctx, certPEM, privkeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to upload certificate file: %w", err)
	} else {
		d.logger.Info("ssl certificate uploaded", slog.Any("result", upres))
	}

	// 绑定空间域名证书
	bindBucketCertResp, err := d.sdkClient.BindBucketCert(ctx, d.config.Domain, upres.CertId)
	d.logger.Debug("sdk request 'kodo.BindCert'", slog.String("params.domain", d.config.Domain), slog.String("params.certId", upres.CertId), slog.Any("response", bindBucketCertResp))
	if err != nil {
		return nil, fmt.Errorf("failed to execute sdk request 'kodo.BindCert': %w", err)
	}

	return &DeployResult{}, nil
}

// 自动接入域名：幂等创建域名 -> 确保 CNAME 解析 -> 轮询生效。
// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
func (d *Deployer) autoOnboardDomain(ctx context.Context) error {
	// 查询域名信息；不存在时创建（幂等：已存在则直接复用）
	domainInfo, err := d.sdkDomain.GetDomainInfo(ctx, d.config.Domain)
	d.logger.Debug("sdk request 'cdn.GetDomainInfo'", slog.String("params.domain", d.config.Domain), slog.Any("response", domainInfo))
	if err != nil {
		createDomainReq := &qiniusdk.CreateDomainRequest{
			Name:     d.config.Domain,
			Type:     "normal",
			Platform: "web",
			GeoCover: "china",
			Protocol: "http",
			Source: &qiniusdk.CreateDomainRequestSource{
				SourceType:        "qiniuBucket",
				SourceQiniuBucket: d.config.Bucket,
			},
		}
		createDomainResp, err := d.sdkDomain.CreateDomain(ctx, createDomainReq)
		d.logger.Debug("sdk request 'cdn.CreateDomain'", slog.String("params.name", d.config.Domain), slog.Any("response", createDomainResp))
		if err != nil {
			// 创建报错时不立即终止：重查一次域名信息，
			// 若此次查询成功，说明域名实际已存在（多为上次执行半途创建成功或平台瞬态误判），视为幂等成功继续流程；
			// 若重查仍失败，则透出原始 CreateDomain 错误（%w 保留平台原始信息，如备案/审核类）
			retriedInfo, retryErr := d.sdkDomain.GetDomainInfo(ctx, d.config.Domain)
			d.logger.Debug("sdk request 'cdn.GetDomainInfo' (retry after create failed)", slog.String("params.domain", d.config.Domain), slog.Any("response", retriedInfo))
			if retryErr != nil {
				return fmt.Errorf("failed to execute sdk request 'cdn.CreateDomain': %w", err)
			}

			domainInfo = retriedInfo
		} else {
			domainInfo, err = d.sdkDomain.GetDomainInfo(ctx, d.config.Domain)
			if err != nil {
				return fmt.Errorf("failed to execute sdk request 'cdn.GetDomainInfo': %w", err)
			}
		}
	}

	// CName 目标为空时（如域名刚创建），轮询等待七牛生成
	if domainInfo.CName == "" {
		domainInfo, err = d.pollDomainInfo(ctx, func(info *qiniusdk.GetDomainInfoResponse) bool { return info.CName != "" })
		if err != nil {
			return err
		}
	}

	// 确保目标域名的 CNAME 解析已指向七牛生成的 CName
	mainDomain, subDomain, err := alidnssdk.SplitMainDomain(d.config.Domain)
	if err != nil {
		return fmt.Errorf("could not split domain '%s': %w", d.config.Domain, err)
	}

	ensureRecordResp, err := d.sdkDNS.EnsureDomainRecord(ctx, &alidnssdk.EnsureDomainRecordRequest{
		MainDomain:        mainDomain,
		SubDomain:         subDomain,
		RecordType:        "CNAME",
		RecordValue:       domainInfo.CName,
		OverwriteExisting: d.config.DnsOverwriteExisting,
	})
	d.logger.Debug("sdk request 'alidns.EnsureDomainRecord'", slog.String("params.mainDomain", mainDomain), slog.String("params.subDomain", subDomain), slog.Any("response", ensureRecordResp))
	if err != nil {
		// 冲突时错误信息包含现有记录值，原样上抛
		return fmt.Errorf("failed to ensure cname record: %w", err)
	}

	// 轮询域名状态直至已生效
	if _, err := d.pollDomainInfo(ctx, func(info *qiniusdk.GetDomainInfoResponse) bool {
		return info.OperatingState == domainOperatingStateSuccess
	}); err != nil {
		return err
	}

	return nil
}

// 轮询域名信息直至满足条件或超时；全程尊重 ctx 取消。
func (d *Deployer) pollDomainInfo(ctx context.Context, done func(info *qiniusdk.GetDomainInfoResponse) bool) (*qiniusdk.GetDomainInfoResponse, error) {
	deadline := time.Now().Add(d.waitVerifyTimeout())

	for {
		info, err := d.sdkDomain.GetDomainInfo(ctx, d.config.Domain)
		d.logger.Debug("sdk request 'cdn.GetDomainInfo'", slog.String("params.domain", d.config.Domain), slog.Any("response", info))
		if err != nil {
			return nil, fmt.Errorf("failed to execute sdk request 'cdn.GetDomainInfo': %w", err)
		}

		if done(info) {
			return info, nil
		}

		// 域名处于终态异常（操作失败/已冻结/已下线）时提前终止轮询，透出平台状态描述
		switch info.OperatingState {
		case domainOperatingStateFailed:
			return nil, fmt.Errorf("domain '%s' entered failed state: %s", d.config.Domain, info.OperatingStateDesc)
		case domainOperatingStateFrozen:
			return nil, fmt.Errorf("domain '%s' entered frozen state: %s", d.config.Domain, info.OperatingStateDesc)
		case domainOperatingStateOfflined:
			return nil, fmt.Errorf("domain '%s' entered offlined state: %s", d.config.Domain, info.OperatingStateDesc)
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("wait for domain '%s' to take effect timed out", d.config.Domain)
		}

		wait := d.pollInterval
		if wait > remaining {
			wait = remaining
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// 获取等待域名校验生效的超时时间。
func (d *Deployer) waitVerifyTimeout() time.Duration {
	if d.config.WaitVerifyTimeout > 0 {
		return time.Duration(d.config.WaitVerifyTimeout) * time.Second
	}
	return defaultWaitVerifyTimeout
}
