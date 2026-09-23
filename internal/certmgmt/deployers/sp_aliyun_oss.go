package deployers

import (
	"fmt"

	"github.com/certimate-go/certimate/internal/domain"
	"github.com/certimate-go/certimate/pkg/core"
	dplyimpl "github.com/certimate-go/certimate/pkg/core/deployer/providers/aliyun-oss"
	xmaps "github.com/certimate-go/certimate/pkg/utils/maps"
)

func init() {
	Registries.MustRegister(domain.DeploymentProviderTypeAliyunOSS, func(options *ProviderFactoryOptions) (core.Deployer, error) {
		credentials := domain.AccessConfigForAliyun{}
		if err := xmaps.Populate(options.ProviderAccessConfig, &credentials); err != nil {
			return nil, fmt.Errorf("failed to populate provider access config: %w", err)
		}

		// 自动接入域名时，需额外引用一条 DNS 提供商的 access 授权配置
		var dnsCredentials domain.AccessConfigForAliyun
		if xmaps.GetBool(options.ProviderExtendedConfig, "autoOnboard") {
			// 校验引擎注入的 DNS 提供商类型：本 provider 仅支持阿里云解析
			if options.ProviderDNSAccessProvider != string(domain.AccessProviderTypeAliyun) {
				return nil, fmt.Errorf("unsupported dns access provider type '%s'", options.ProviderDNSAccessProvider)
			}

			if err := xmaps.Populate(options.ProviderDNSAccessConfig, &dnsCredentials); err != nil {
				return nil, fmt.Errorf("failed to populate provider dns access config: %w", err)
			}
		}

		provider, err := dplyimpl.NewDeployer(&dplyimpl.DeployerConfig{
			AccessKeyId:     credentials.AccessKeyId,
			AccessKeySecret: credentials.AccessKeySecret,
			ResourceGroupId: credentials.ResourceGroupId,
			Region:          xmaps.GetString(options.ProviderExtendedConfig, "region"),
			Bucket:          xmaps.GetString(options.ProviderExtendedConfig, "bucket"),
			Domain:          xmaps.GetString(options.ProviderExtendedConfig, "domain"),

			AutoOnboard:          xmaps.GetBool(options.ProviderExtendedConfig, "autoOnboard"),
			DnsAccessKeyId:       dnsCredentials.AccessKeyId,
			DnsAccessKeySecret:   dnsCredentials.AccessKeySecret,
			DnsOverwriteExisting: xmaps.GetBool(options.ProviderExtendedConfig, "dnsOverwriteExisting"),
			WaitVerifyTimeout:    xmaps.GetInt32(options.ProviderExtendedConfig, "waitVerifyTimeout"),
		})
		return provider, err
	})
}
