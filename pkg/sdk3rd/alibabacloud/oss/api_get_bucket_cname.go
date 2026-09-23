package oss

import (
	"context"
	"fmt"
	"net/http"
)

type ListBucketCnameRequest struct{}

type ListBucketCnameResponseCname struct {
	Domain       *string `json:",omitempty" xml:"Domain,omitempty"`
	LastModified *string `json:",omitempty" xml:"LastModified,omitempty"`
	Status       *string `json:",omitempty" xml:"Status,omitempty"`
	IsWildCard   *bool   `json:",omitempty" xml:"IsWildCard,omitempty"`

	Certificate *ListBucketCnameResponseCertificate `json:",omitempty" xml:"Certificate,omitempty"`
}

type ListBucketCnameResponseCertificate struct {
	Type           *string `json:",omitempty" xml:"Type,omitempty"`
	CertId         *string `json:",omitempty" xml:"CertId,omitempty"`
	Status         *string `json:",omitempty" xml:"Status,omitempty"`
	CreationDate   *string `json:",omitempty" xml:"CreationDate,omitempty"`
	Fingerprint    *string `json:",omitempty" xml:"Fingerprint,omitempty"`
	ValidStartDate *string `json:",omitempty" xml:"ValidStartDate,omitempty"`
	ValidEndDate   *string `json:",omitempty" xml:"ValidEndDate,omitempty"`
}

type ListBucketCnameResponse struct {
	sdkResponseBase

	Bucket *string `json:",omitempty" xml:"Bucket,omitempty"`
	Owner  *string `json:",omitempty" xml:"Owner,omitempty"`

	Cnames []ListBucketCnameResponseCname `json:",omitempty" xml:"Cname,omitempty"`
}

func (c *Client) ListBucketCname(req *ListBucketCnameRequest) (*ListBucketCnameResponse, error) {
	return c.ListBucketCnameWithContext(context.Background(), req)
}

func (c *Client) ListBucketCnameWithContext(ctx context.Context, req *ListBucketCnameRequest) (*ListBucketCnameResponse, error) {
	// 注意：GET 请求无请求体，`req` 仅作占位
	httpreq, err := c.newRequest(http.MethodGet, "/?cname", nil, fmt.Sprintf("/%s/", c.bucket))
	if err != nil {
		return nil, err
	} else {
		httpreq.SetContext(ctx)
	}

	result := &ListBucketCnameResponse{}
	if _, err := c.doRequestWithResult(httpreq, result); err != nil {
		return result, err
	}

	return result, nil
}
