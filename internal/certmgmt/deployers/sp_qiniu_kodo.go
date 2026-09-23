package deployers

import (
	"fmt"

	"github.com/certimate-go/certimate/internal/domain"
	"github.com/certimate-go/certimate/pkg/core"
	dplyimpl "github.com/certimate-go/certimate/pkg/core/deployer/providers/qiniu-kodo"
	xmaps "github.com/certimate-go/certimate/pkg/utils/maps"
)

func init() {
	Registries.MustRegister(domain.DeploymentProviderTypeQiniuKodo, func(options *ProviderFactoryOptions) (core.Deployer, error) {
		credentials := domain.AccessConfigForQiniu{}
		if err := xmaps.Populate(options.ProviderAccessConfig, &credentials); err != nil {
			return nil, fmt.Errorf("failed to populate provider access config: %w", err)
		}

		config := &dplyimpl.DeployerConfig{
			AccessKey: credentials.AccessKey,
			SecretKey: credentials.SecretKey,
			Bucket:    xmaps.GetString(options.ProviderExtendedConfig, "bucket"),
			Domain:    xmaps.GetString(options.ProviderExtendedConfig, "domain"),

			AutoOnboard:          xmaps.GetBool(options.ProviderExtendedConfig, "autoOnboard"),
			DnsOverwriteExisting: xmaps.GetBool(options.ProviderExtendedConfig, "dnsOverwriteExisting"),
			WaitVerifyTimeout:    xmaps.GetInt32(options.ProviderExtendedConfig, "waitVerifyTimeout"),
		}

		// 自动接入域名开启时，加载 DNS 提供商授权配置
		if config.AutoOnboard {
			// 校验 DNS access 的提供商类型：v1 仅支持阿里云 DNS（与 OSS 共用 AK/SK）
			if options.ProviderDNSAccessProvider != string(domain.AccessProviderTypeAliyun) {
				return nil, fmt.Errorf("unsupported dns access provider type '%s', only '%s' is supported", options.ProviderDNSAccessProvider, string(domain.AccessProviderTypeAliyun))
			}

			dnsCredentials := domain.AccessConfigForAliyun{}
			if err := xmaps.Populate(options.ProviderDNSAccessConfig, &dnsCredentials); err != nil {
				return nil, fmt.Errorf("failed to populate provider dns access config: %w", err)
			}

			config.DnsAccessKeyId = dnsCredentials.AccessKeyId
			config.DnsAccessKeySecret = dnsCredentials.AccessKeySecret
		}

		provider, err := dplyimpl.NewDeployer(config)
		return provider, err
	})
}
