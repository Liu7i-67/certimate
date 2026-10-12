package aliyundns

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/certimate-go/certimate/pkg/core"
	"github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/alidns"
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
	// 完整解析记录名（如 bucket.taoxiplan.com）。
	Domain string `json:"domain"`
	// 记录类型（如 A、AAAA、CNAME、TXT）。
	RecordType string `json:"recordType"`
	// 记录值（如 A 记录的服务器 IP）。
	RecordValue string `json:"recordValue"`
	// 已存在且值不同时是否覆盖更新。
	OverwriteExisting bool `json:"overwriteExisting,omitempty"`
}

type Deployer struct {
	config    *DeployerConfig
	logger    *slog.Logger
	sdkClient *alidns.Client
}

var _ Provider = (*Deployer)(nil)

func NewDeployer(config *DeployerConfig) (*Deployer, error) {
	if config == nil {
		return nil, fmt.Errorf("the configuration of the deployer provider is nil")
	}

	client, err := alidns.NewClient(config.AccessKeyId, config.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("could not create sdk client: %w", err)
	}

	return &Deployer{
		config:    config,
		logger:    slog.Default(),
		sdkClient: client,
	}, nil
}

func (d *Deployer) SetLogger(logger *slog.Logger) {
	if logger == nil {
		d.logger = slog.New(slog.DiscardHandler)
	} else {
		d.logger = logger
	}
}

func (d *Deployer) Deploy(ctx context.Context, certPEM, privkeyPEM string) (*DeployResult, error) {
	// 本 provider 不消费证书内容，仅编排解析记录；
	// 挂在部署节点上是为了复用工作流的执行时序与重试能力。
	mainDomain, subDomain, err := alidns.SplitMainDomain(d.config.Domain)
	if err != nil {
		return nil, fmt.Errorf("failed to split domain: %w", err)
	}

	res, err := d.sdkClient.EnsureDomainRecord(ctx, &alidns.EnsureDomainRecordRequest{
		MainDomain:        mainDomain,
		SubDomain:         subDomain,
		RecordType:        d.config.RecordType,
		RecordValue:       d.config.RecordValue,
		OverwriteExisting: d.config.OverwriteExisting,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure dns record: %w", err)
	}

	d.logger.Info("dns record ensured",
		slog.String("recordType", d.config.RecordType),
		slog.String("mainDomain", mainDomain),
		slog.String("subDomain", subDomain),
		slog.String("recordValue", d.config.RecordValue),
		slog.Any("result", res))

	return &DeployResult{}, nil
}
