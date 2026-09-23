package oss

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

// ServiceError 表示阿里云 OSS 服务端返回的非 2xx 错误响应。
type ServiceError struct {
	// HTTP 状态码。
	StatusCode int
	// OSS 错误码（即错误响应体中的 <Code> 字段；无法解析时为空）。
	Code string
	// OSS 错误消息（即错误响应体中的 <Message> 字段；无法解析时为空）。
	Message string
	// 原始响应体内容。
	Body string
}

func (e *ServiceError) Error() string {
	// 与既有错误信息格式保持一致，避免影响调用方对错误文本的匹配
	return fmt.Sprintf("sdkerr: unexpected status code: %d (resp: %s)", e.StatusCode, e.Body)
}

// ossErrorResult 表示 OSS 错误响应体的 XML 结构。
type ossErrorResult struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// 从 resty 响应构造 ServiceError，并尽力解析错误码与错误消息。
func newServiceError(resp *resty.Response) *ServiceError {
	serr := &ServiceError{
		StatusCode: resp.StatusCode(),
		Body:       resp.String(),
	}

	var parsed ossErrorResult
	if err := xml.Unmarshal(resp.Body(), &parsed); err == nil {
		serr.Code = parsed.Code
		serr.Message = parsed.Message
	}

	return serr
}

// IsNoCnameError 判断错误是否为「Bucket 未绑定任何 Cname」的 404 空态
// （即 HTTP 状态码为 404 且 OSS 错误码为 NoSuchCname）。
// 该情形通常出现在全新 Bucket 接入首个自定义域名时，属正常空态而非异常。
// REF: https://help.aliyun.com/zh/oss/developer-reference/listcname
func IsNoCnameError(err error) bool {
	var serr *ServiceError
	if !errors.As(err, &serr) {
		return false
	}

	return serr.StatusCode == http.StatusNotFound && serr.Code == "NoSuchCname"
}
