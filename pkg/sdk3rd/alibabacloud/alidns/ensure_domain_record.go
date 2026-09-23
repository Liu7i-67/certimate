package alidns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	alidns20150109 "github.com/alibabacloud-go/alidns-20150109/v4/client"
	"github.com/alibabacloud-go/tea/dara"
)

// ErrRecordConflict 表示解析记录已存在且指向不同的值（默认不覆盖）。
// 调用方可通过 errors.Is 判断；错误信息中包含现有记录值。
var ErrRecordConflict = errors.New("alidns: record already exists with a different value")

type EnsureDomainRecordRequest struct {
	// 主域名（如 taoxiplan.com）。
	MainDomain string
	// 主机记录 RR（如 lingji、_dnsauth.lingji）；空串表示根（内部转 "@"）。
	SubDomain string
	// 记录类型，如 "CNAME"、"TXT"。
	RecordType string
	// 目标记录值。
	RecordValue string
	// 已存在且值不同时是否覆盖更新。
	OverwriteExisting bool
}

type EnsureDomainRecordResult struct {
	// 记录 ID。
	RecordId string
	// 是否新建。
	Created bool
	// 是否覆盖更新。
	Updated bool
	// 是否已存在且值一致（跳过）。
	Skipped bool
}

// 确保指定的解析记录存在：
//
//   - 无记录   -> 新建（Created=true）
//   - 值一致   -> 跳过（Skipped=true）
//   - 值不同   -> OverwriteExisting 为 true 时更新（Updated=true），
//     否则返回包级错误变量 ErrRecordConflict（错误信息含现有记录值）
//
// ctx 取消全程透传。
func (c *Client) EnsureDomainRecord(ctx context.Context, req *EnsureDomainRecordRequest) (*EnsureDomainRecordResult, error) {
	if req == nil {
		return nil, fmt.Errorf("the request is nil")
	}
	if req.MainDomain == "" {
		return nil, fmt.Errorf("config `mainDomain` is required")
	}
	if req.RecordType == "" {
		return nil, fmt.Errorf("config `recordType` is required")
	}

	// 阿里云 DNS 中根域名的主机记录用 "@" 表示
	subDomain := req.SubDomain
	if subDomain == "" {
		subDomain = "@"
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 查询现有解析记录
	// REF: https://help.aliyun.com/zh/dns/describedomainrecords
	describeResp, err := c.sdkClient.DescribeDomainRecords(&alidns20150109.DescribeDomainRecordsRequest{
		DomainName: dara.String(req.MainDomain),
		RRKeyWord:  dara.String(subDomain),
		Type:       dara.String(req.RecordType),
		PageSize:   dara.Int64(100),
	})
	if err != nil {
		return nil, fmt.Errorf("alidns: failed to execute sdk request 'DescribeDomainRecords': %w", err)
	}

	var existing *alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord
	if describeResp != nil && describeResp.Body != nil && describeResp.Body.DomainRecords != nil {
		for _, record := range describeResp.Body.DomainRecords.Record {
			if record == nil {
				continue
			}
			// RRKeyWord 为关键字匹配，需精确比对主机记录与记录类型；
			// 域名不区分大小写，主机记录按忽略大小写方式比较（记录值不在此比较，保留原样大小写）
			if strings.EqualFold(dara.StringValue(record.RR), subDomain) && dara.StringValue(record.Type) == req.RecordType {
				existing = record
				break
			}
		}
	}

	// 无记录 -> 新建
	if existing == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		addResp, err := c.sdkClient.AddDomainRecord(&alidns20150109.AddDomainRecordRequest{
			DomainName: dara.String(req.MainDomain),
			RR:         dara.String(subDomain),
			Type:       dara.String(req.RecordType),
			Value:      dara.String(req.RecordValue),
		})
		if err != nil {
			return nil, fmt.Errorf("alidns: failed to execute sdk request 'AddDomainRecord': %w", err)
		}

		recordId := ""
		if addResp != nil && addResp.Body != nil {
			recordId = dara.StringValue(addResp.Body.RecordId)
		}
		return &EnsureDomainRecordResult{RecordId: recordId, Created: true}, nil
	}

	// 已存在且值一致 -> 跳过
	if dara.StringValue(existing.Value) == req.RecordValue {
		return &EnsureDomainRecordResult{RecordId: dara.StringValue(existing.RecordId), Skipped: true}, nil
	}

	// 已存在且值不同 -> 按需覆盖更新
	if !req.OverwriteExisting {
		return nil, fmt.Errorf("%w: domain '%s', RR '%s', type '%s', existing value '%s'", ErrRecordConflict, req.MainDomain, subDomain, req.RecordType, dara.StringValue(existing.Value))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	updateResp, err := c.sdkClient.UpdateDomainRecord(&alidns20150109.UpdateDomainRecordRequest{
		RecordId: existing.RecordId,
		RR:       dara.String(subDomain),
		Type:     dara.String(req.RecordType),
		Value:    dara.String(req.RecordValue),
	})
	if err != nil {
		return nil, fmt.Errorf("alidns: failed to execute sdk request 'UpdateDomainRecord': %w", err)
	}

	recordId := dara.StringValue(existing.RecordId)
	if updateResp != nil && updateResp.Body != nil {
		recordId = dara.StringValue(updateResp.Body.RecordId)
	}
	return &EnsureDomainRecordResult{RecordId: recordId, Updated: true}, nil
}
