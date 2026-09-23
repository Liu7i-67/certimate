package qiniu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/client"
)

type CdnManager struct {
	client *client.Client
}

func NewCdnManager(mac *auth.Credentials) *CdnManager {
	if mac == nil {
		mac = auth.Default()
	}

	client := &client.Client{Client: &http.Client{Transport: newTransport(mac, nil)}}
	return &CdnManager{client: client}
}

type GetDomainListResponse struct {
	Code    *int    `json:"code,omitempty"`
	Error   *string `json:"error,omitempty"`
	Marker  string  `json:"marker"`
	Domains []*struct {
		Name               string `json:"name"`
		Type               string `json:"type"`
		CName              string `json:"cname"`
		OperatingState     string `json:"operatingState"`
		OperatingStateDesc string `json:"operatingStateDesc"`
		CreateAt           string `json:"createAt"`
		ModifyAt           string `json:"modifyAt"`
	} `json:"domains"`
}

func (m *CdnManager) GetDomainList(ctx context.Context, marker string, limit int) (*GetDomainListResponse, error) {
	query := url.Values{}
	if marker != "" {
		query.Set("marker", marker)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	resp := new(GetDomainListResponse)
	if err := m.client.Call(ctx, resp, http.MethodGet, urlf("domain?%s", query.Encode()), nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type GetDomainInfoResponse struct {
	Code  *int    `json:"code,omitempty"`
	Error *string `json:"error,omitempty"`
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	CName string  `json:"cname"`
	Https *struct {
		CertID      string `json:"certId"`
		ForceHttps  bool   `json:"forceHttps"`
		Http2Enable bool   `json:"http2Enable"`
	} `json:"https"`
	PareDomain         string `json:"pareDomain"`
	OperationType      string `json:"operationType"`
	OperatingState     string `json:"operatingState"`
	OperatingStateDesc string `json:"operatingStateDesc"`
	CreateAt           string `json:"createAt"`
	ModifyAt           string `json:"modifyAt"`
}

func (m *CdnManager) GetDomainInfo(ctx context.Context, domain string) (*GetDomainInfoResponse, error) {
	resp := new(GetDomainInfoResponse)
	if err := m.client.Call(ctx, resp, http.MethodGet, urlf("domain/%s", domain), nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type ModifyDomainHttpsConfRequest struct {
	CertID      string `json:"certId"`
	ForceHttps  bool   `json:"forceHttps"`
	Http2Enable bool   `json:"http2Enable"`
}

type ModifyDomainHttpsConfResponse struct {
	Code  *int    `json:"code,omitempty"`
	Error *string `json:"error,omitempty"`
}

func (m *CdnManager) ModifyDomainHttpsConf(ctx context.Context, domain string, certId string, forceHttps bool, http2Enable bool) (*ModifyDomainHttpsConfResponse, error) {
	req := &ModifyDomainHttpsConfRequest{
		CertID:      certId,
		ForceHttps:  forceHttps,
		Http2Enable: http2Enable,
	}
	resp := new(ModifyDomainHttpsConfResponse)
	if err := m.client.CallWithJson(ctx, resp, http.MethodPut, urlf("domain/%s/httpsconf", domain), nil, req); err != nil {
		return nil, err
	}
	return resp, nil
}

type EnableDomainHttpsRequest struct {
	CertID      string `json:"certId"`
	ForceHttps  bool   `json:"forceHttps"`
	Http2Enable bool   `json:"http2Enable"`
}

type EnableDomainHttpsResponse struct {
	Code  *int    `json:"code,omitempty"`
	Error *string `json:"error,omitempty"`
}

func (m *CdnManager) EnableDomainHttps(ctx context.Context, domain string, certId string, forceHttps bool, http2Enable bool) (*EnableDomainHttpsResponse, error) {
	req := &EnableDomainHttpsRequest{
		CertID:      certId,
		ForceHttps:  forceHttps,
		Http2Enable: http2Enable,
	}
	resp := new(EnableDomainHttpsResponse)
	if err := m.client.CallWithJson(ctx, resp, http.MethodPut, urlf("domain/%s/sslize", domain), nil, req); err != nil {
		return nil, err
	}
	return resp, nil
}

// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
type CreateDomainRequestSource struct {
	// 回源类型；"qiniuBucket" 表示回源七牛云存储 bucket。
	SourceType string `json:"sourceType"`
	// 回源的七牛云存储 bucket 名称；sourceType 为 "qiniuBucket" 时必填。
	SourceQiniuBucket string `json:"sourceQiniuBucket,omitempty"`
	// 回源域名；sourceType 为 "domain" 时必填。
	SourceDomain string `json:"sourceDomain,omitempty"`
}

// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
type CreateDomainRequestCache struct {
	// 缓存规则列表。
	CacheControls []CreateDomainRequestCacheControl `json:"cacheControls"`
}

// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
type CreateDomainRequestCacheControl struct {
	// 缓存时长；0 表示遵循源站。
	Time int64 `json:"time"`
	// 时间单位（秒/分/时/天 对应的数值单位）。
	Timeunit int64 `json:"timeunit"`
	// 规则类型；"all" 表示全局规则。
	Type string `json:"type"`
}

// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
type CreateDomainRequest struct {
	// 加速域名（泛域名以 "." 开头）。
	Name string `json:"name"`
	// 域名类型："normal"、"wildcard"。
	Type string `json:"type"`
	// 平台类型："web"、"download"、"vod"、"dynamic"。
	Platform string `json:"platform"`
	// 加速区域："china"、"foreign"、"global"；为 "china"/"global" 时域名需已完成 ICP 备案。
	GeoCover string `json:"geoCover"`
	// 协议类型："http"、"https"。
	Protocol string `json:"protocol"`
	// 回源参数（将域名关联到七牛 bucket 时使用 sourceType="qiniuBucket"）。
	Source *CreateDomainRequestSource `json:"source"`
	// 缓存配置；平台为动态加速时可缺省，其余平台必填。time=0 表示遵循源站，type="all" 表示全局规则。
	Cache *CreateDomainRequestCache `json:"cache,omitempty"`
	// ICP 备案号；创建域名返回 500230（备案校验失败）等错误时按需传入。
	RegisterNo string `json:"registerNo,omitempty"`
}

type CreateDomainResponse struct {
	Code  *int    `json:"code,omitempty"`
	Error *string `json:"error,omitempty"`
}

// REF: https://developer.qiniu.com/fusion/4246/the-domain-name
// 创建域名：域名为资源名，需置于请求路径中（POST /domain/{name}）；域名仅由字母数字与点号等组成，无需转义。
func (m *CdnManager) CreateDomain(ctx context.Context, req *CreateDomainRequest) (*CreateDomainResponse, error) {
	resp := new(CreateDomainResponse)
	if err := m.client.CallWithJson(ctx, resp, http.MethodPost, urlf("domain/%s", req.Name), nil, req); err != nil {
		return nil, err
	}
	return resp, nil
}
