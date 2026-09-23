package oss

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
)

type CreateCnameTokenRequest struct {
	XMLName xml.Name                      `json:"-"          xml:"BucketCnameConfiguration"`
	Cname   *CreateCnameTokenRequestCname `json:",omitempty" xml:"Cname,omitempty"`
}

type CreateCnameTokenRequestCname struct {
	Domain *string `json:",omitempty" xml:"Domain,omitempty"`
}

type CreateCnameTokenResponse struct {
	sdkResponseBase

	Bucket     *string `json:",omitempty" xml:"Bucket,omitempty"`
	Cname      *string `json:",omitempty" xml:"Cname,omitempty"`
	Token      *string `json:",omitempty" xml:"Token,omitempty"`
	ExpireTime *string `json:",omitempty" xml:"ExpireTime,omitempty"`
}

func (c *Client) CreateCnameToken(req *CreateCnameTokenRequest) (*CreateCnameTokenResponse, error) {
	return c.CreateCnameTokenWithContext(context.Background(), req)
}

func (c *Client) CreateCnameTokenWithContext(ctx context.Context, req *CreateCnameTokenRequest) (*CreateCnameTokenResponse, error) {
	httpreq, err := c.newRequest(http.MethodPost, "/?cname&comp=token", req, fmt.Sprintf("/%s/", c.bucket))
	if err != nil {
		return nil, err
	} else {
		httpreq.SetContext(ctx)
	}

	result := &CreateCnameTokenResponse{}
	if _, err := c.doRequestWithResult(httpreq, result); err != nil {
		return result, err
	}

	return result, nil
}
