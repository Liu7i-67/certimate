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

// 七牛域名归属权验证状态（GET /domain/{name}/verify/info 的 state 取值）。
const (
	domainVerifyStateDoing   = "doing"   // 待验证
	domainVerifyStateSuccess = "success" // 已通过验证
	domainVerifyStateNoNeed  = "no_need" // 无需验证
)

// 七牛 CDN 加速区域（CreateDomain 的 geoCover 取值）。
const (
	geoCoverChina   = "china"   // 中国大陆（需已完成 ICP 备案）
	geoCoverForeign = "foreign" // 中国大陆以外（免备案）
	geoCoverGlobal  = "global"  // 全球（需已完成 ICP 备案）
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

	// CDN 加速区域："china"（默认，需已完成 ICP 备案）| "foreign"（中国大陆以外，免备案）| "global"（全球，需已完成 ICP 备案）；空串取默认值。
	GeoCover string `json:"geoCover,omitempty"`
	// ICP 备案号；加速区域为 "china"/"global" 时平台可能要求提供，可空。
	IcpRegisterNo string `json:"icpRegisterNo,omitempty"`
}

// 七牛域名管理客户端（测试缝）。
type qiniuDomainClient interface {
	GetDomainInfo(ctx context.Context, domain string) (*qiniusdk.GetDomainInfoResponse, error)
	CreateDomain(ctx context.Context, req *qiniusdk.CreateDomainRequest) (*qiniusdk.CreateDomainResponse, error)
	// EnableDomainHttps 对应 CDN 的 sslize 接口（PUT /domain/{name}/sslize）：
	// CDN 加速域名需先开启 HTTPS 才能挂证书，sslize 一步完成「开启 HTTPS + 绑定证书」且幂等
	// （httpsconf 对未开启 HTTPS 的域名返回 400/400302「更改证书失败」，此为该问题的解法）。
	EnableDomainHttps(ctx context.Context, domain string, certId string, forceHttps bool, http2Enable bool) (*qiniusdk.EnableDomainHttpsResponse, error)
	// GetDomainVerifyInfo 对应 CDN 的归属权验证信息接口（GET /domain/{name}/verify/info?product=cdn）：
	// 返回归属权验证状态与 DNS 验证挑战（域名不存在时也可调用）。
	GetDomainVerifyInfo(ctx context.Context, domain string) (*qiniusdk.GetDomainVerifyInfoResponse, error)
	// CheckDomainVerify 对应 CDN 的归属权校验接口（POST /domain/{name}/verify/check）：
	// 触发平台主动查询 DNS 验证记录，记录未生效时返回非 200（错误），HTTP 200 即通过。
	CheckDomainVerify(ctx context.Context, domain string) (*qiniusdk.CheckDomainVerifyResponse, error)
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
	// 默认加速区域（china：需已完成 ICP 备案）。
	defaultGeoCover = geoCoverChina
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

	// 加速区域合法性校验（空串取默认值；不在此处对"china 必须有备案号"做硬校验，备案校验交给平台错误透出）
	switch config.GeoCover {
	case "", geoCoverChina, geoCoverForeign, geoCoverGlobal:
	default:
		return nil, fmt.Errorf("config `geoCover` is invalid: %q (must be one of: china, foreign, global)", config.GeoCover)
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

	// 绑定证书：autoOnboard 通过 CDN API 接入的是 CDN 加速域名，须走 CDN 的证书绑定接口
	// （kodo.BindCert 仅支持源站域名，对 CDN 加速域名报 domaintype is not allowed）；
	// 关闭时保持原有绑定路径，语句顺序与原逻辑完全一致
	if d.config.AutoOnboard {
		// 绑定证书用 sslize（EnableDomainHttps）而非 httpsconf：CDN 加速域名需先 sslize 开启 HTTPS
		// 才能挂证书，sslize 一步完成「开启 HTTPS + 绑定证书」，且对已开启域名重复调用为幂等更新
		// （httpsconf 对未开启 HTTPS 的域名返回 400/400302「更改证书失败」）。
		// 不强制 HTTPS 跳转、不开启 HTTP/2，保持保守默认
		enableDomainHttpsResp, err := d.sdkDomain.EnableDomainHttps(ctx, d.config.Domain, upres.CertId, false, false)
		d.logger.Debug("sdk request 'cdn.EnableDomainHttps'", slog.String("params.domain", d.config.Domain), slog.String("params.certId", upres.CertId), slog.Any("response", enableDomainHttpsResp))
		if err != nil {
			return nil, fmt.Errorf("failed to execute sdk request 'cdn.EnableDomainHttps': %w", err)
		}
	} else {
		// 绑定空间域名证书
		bindBucketCertResp, err := d.sdkClient.BindBucketCert(ctx, d.config.Domain, upres.CertId)
		d.logger.Debug("sdk request 'kodo.BindCert'", slog.String("params.domain", d.config.Domain), slog.String("params.certId", upres.CertId), slog.Any("response", bindBucketCertResp))
		if err != nil {
			return nil, fmt.Errorf("failed to execute sdk request 'kodo.BindCert': %w", err)
		}
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
		// 域名不存在时，先确保归属权验证通过（七牛对未验证归属的域名拒绝创建，创建时报 400932）
		if err := d.ensureDomainOwnership(ctx); err != nil {
			return err
		}

		createDomainReq := &qiniusdk.CreateDomainRequest{
			Name:       d.config.Domain,
			Type:       "normal",
			Platform:   "web",
			GeoCover:   d.geoCover(),
			Protocol:   "http",
			RegisterNo: d.config.IcpRegisterNo,
			Source: &qiniusdk.CreateDomainRequestSource{
				SourceType:        "qiniuBucket",
				SourceQiniuBucket: d.config.Bucket,
			},
			// web 平台缓存配置必填（缺失时七牛返回 400/400309），此处显式带上默认值：全局规则、遵循源站
			Cache: &qiniusdk.CreateDomainRequestCache{
				CacheControls: []qiniusdk.CreateDomainRequestCacheControl{{Time: 0, Timeunit: 0, Type: "all"}},
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

// 确保域名归属权验证通过（七牛在创建 CDN 加速域名前校验域名归属，未验证时创建返回 400932）。
//
// 流程：查询验证信息 → 若处于待验证状态，按响应中的主机记录与挂载基准域写入 TXT 验证记录
// （固定覆盖、不受 DnsOverwriteExisting 约束——归属挑战记录属内部自管记录，残留旧值必然阻塞校验）→
// 轮询触发平台校验直至通过。
//
// 宽容策略：查询验证信息出错时不阻断流程（已验证域名 / 端点不适用等场景），
// 真正缺失归属验证时由后续 CreateDomain 的平台原始错误兜底。
// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
func (d *Deployer) ensureDomainOwnership(ctx context.Context) error {
	verifyInfo, err := d.sdkDomain.GetDomainVerifyInfo(ctx, d.config.Domain)
	d.logger.Debug("sdk request 'cdn.GetDomainVerifyInfo'", slog.String("params.domain", d.config.Domain), slog.Any("response", verifyInfo), slog.Any("error", err))
	if err != nil {
		d.logger.Warn("could not query domain verify info, skip domain ownership verification", slog.String("domain", d.config.Domain), slog.Any("error", err))
		return nil
	}

	// 已通过验证 / 无需验证 / 无 DNS 验证挑战信息时无需处理
	if verifyInfo.State == domainVerifyStateSuccess || verifyInfo.State == domainVerifyStateNoNeed || verifyInfo.Dns == nil {
		return nil
	}

	// DNS 验证记录的完整 FQDN = 挑战主机记录 + 挂载基准域；基准域可能是根域名，与部署域名不同
	fullHost := verifyInfo.Dns.Host + "." + verifyInfo.Domain
	mainDomain, subDomain, err := alidnssdk.SplitMainDomain(fullHost)
	if err != nil {
		return fmt.Errorf("could not split domain '%s': %w", fullHost, err)
	}

	recordType := verifyInfo.Dns.RecordType
	if recordType == "" {
		recordType = "TXT"
	}

	ensureRecordResp, err := d.sdkDNS.EnsureDomainRecord(ctx, &alidnssdk.EnsureDomainRecordRequest{
		MainDomain:        mainDomain,
		SubDomain:         subDomain,
		RecordType:        recordType,
		RecordValue:       verifyInfo.Dns.RecordValue,
		OverwriteExisting: true,
	})
	d.logger.Debug("sdk request 'alidns.EnsureDomainRecord' (domain ownership)", slog.String("params.mainDomain", mainDomain), slog.String("params.subDomain", subDomain), slog.Any("response", ensureRecordResp))
	if err != nil {
		// 冲突等错误原样上抛（ErrRecordConflict 的错误信息已含现有记录值），并附 TXT 全名便于排查
		return fmt.Errorf("failed to ensure domain ownership txt record '%s': %w", fullHost, err)
	}

	// 轮询触发平台校验直至通过：平台主动查询 DNS 解析记录，记录未生效时接口报错，按间隔重试
	deadline := time.Now().Add(d.waitVerifyTimeout())

	for {
		checkResp, err := d.sdkDomain.CheckDomainVerify(ctx, d.config.Domain)
		d.logger.Debug("sdk request 'cdn.CheckDomainVerify'", slog.String("params.domain", d.config.Domain), slog.Any("response", checkResp), slog.Any("error", err))
		if err == nil {
			d.logger.Info("domain ownership verified", slog.String("domain", d.config.Domain), slog.String("txt.host", fullHost))
			return nil
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("domain ownership verification aborted: please check the TXT record '%s' (value '%s') manually: %w", fullHost, verifyInfo.Dns.RecordValue, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("wait for domain ownership verification timed out: please check the TXT record '%s' (value '%s') manually", fullHost, verifyInfo.Dns.RecordValue)
		}

		wait := d.pollInterval
		if wait > remaining {
			wait = remaining
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("domain ownership verification aborted: please check the TXT record '%s' (value '%s') manually: %w", fullHost, verifyInfo.Dns.RecordValue, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// 获取加速区域配置；空串取默认值。
func (d *Deployer) geoCover() string {
	if d.config.GeoCover != "" {
		return d.config.GeoCover
	}
	return defaultGeoCover
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
