// Package alidns 阿里云解析 DNS（Alibaba Cloud DNS）客户端封装。
//
// 底层使用官方 SDK github.com/alibabacloud-go/alidns-20150109，
// 并在此基础上提供「确保解析记录存在」等便捷方法。
package alidns

import (
	alidns20150109 "github.com/alibabacloud-go/alidns-20150109/v4/client"
	openapiutil "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	"github.com/alibabacloud-go/tea/dara"
)

const defaultEndpoint = "alidns.aliyuncs.com"

// 官方 SDK 客户端接口（非导出测试缝）：方法签名与官方 client 对应方法保持一致，
// 便于单元测试注入 fake 实现。
type sdkClient interface {
	DescribeDomainRecords(request *alidns20150109.DescribeDomainRecordsRequest) (_result *alidns20150109.DescribeDomainRecordsResponse, _err error)
	AddDomainRecord(request *alidns20150109.AddDomainRecordRequest) (_result *alidns20150109.AddDomainRecordResponse, _err error)
	UpdateDomainRecord(request *alidns20150109.UpdateDomainRecordRequest) (_result *alidns20150109.UpdateDomainRecordResponse, _err error)
}

type Client struct {
	sdkClient sdkClient
}

func NewClient(accessKeyId, accessKeySecret string) (*Client, error) {
	config := &openapiutil.Config{
		AccessKeyId:     dara.String(accessKeyId),
		AccessKeySecret: dara.String(accessKeySecret),
		Endpoint:        dara.String(defaultEndpoint),
	}

	sdkClient, err := alidns20150109.NewClient(config)
	if err != nil {
		return nil, err
	}

	return &Client{sdkClient: sdkClient}, nil
}
