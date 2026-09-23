package oss

import (
	"context"
	"fmt"
	"net/http"
)

type GetCnameTokenResponse struct {
	sdkResponseBase

	Bucket     *string `json:",omitempty" xml:"Bucket,omitempty"`
	Cname      *string `json:",omitempty" xml:"Cname,omitempty"`
	Token      *string `json:",omitempty" xml:"Token,omitempty"`
	ExpireTime *string `json:",omitempty" xml:"ExpireTime,omitempty"`
}

func (c *Client) GetCnameToken(cname string) (*GetCnameTokenResponse, error) {
	return c.GetCnameTokenWithContext(context.Background(), cname)
}

func (c *Client) GetCnameTokenWithContext(ctx context.Context, cname string) (*GetCnameTokenResponse, error) {
	// 注意：`cname` 作为查询参数携带待查询的域名值，
	// 不能与子资源标记 `cname`（空值）同时出现，否则会因重复键被合并。
	httpreq, err := c.newRequest(http.MethodGet, fmt.Sprintf("/?comp=token&cname=%s", cname), nil, fmt.Sprintf("/%s/", c.bucket))
	if err != nil {
		return nil, err
	} else {
		httpreq.SetContext(ctx)
	}

	result := &GetCnameTokenResponse{}
	if _, err := c.doRequestWithResult(httpreq, result); err != nil {
		return result, err
	}

	return result, nil
}
