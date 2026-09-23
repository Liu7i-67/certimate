package alidns

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	alidns20150109 "github.com/alibabacloud-go/alidns-20150109/v4/client"
	"github.com/alibabacloud-go/tea/dara"
	"github.com/stretchr/testify/require"
)

/* -------------------- 测试缝的 fake 实现 -------------------- */

// fakeSDKClient：官方 SDK 客户端的 fake 实现（注入非导出接口 sdkClient）。
type fakeSDKClient struct {
	mu sync.Mutex

	// DescribeDomainRecords 的预置返回（现有解析记录列表）
	describeRecords []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord
	describeErr     error

	// AddDomainRecord / UpdateDomainRecord 的预置返回与错误
	addErr      error
	addRecordId string
	updateErr   error

	// 调用计数
	describeCalls int
	addCalls      int
	updateCalls   int

	// 捕获的最近一次请求参数
	lastDescribe *alidns20150109.DescribeDomainRecordsRequest
	lastAdd      *alidns20150109.AddDomainRecordRequest
	lastUpdate   *alidns20150109.UpdateDomainRecordRequest
}

var _ sdkClient = (*fakeSDKClient)(nil)

func (f *fakeSDKClient) DescribeDomainRecords(request *alidns20150109.DescribeDomainRecordsRequest) (_result *alidns20150109.DescribeDomainRecordsResponse, _err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describeCalls++
	f.lastDescribe = request

	if f.describeErr != nil {
		return nil, f.describeErr
	}

	return &alidns20150109.DescribeDomainRecordsResponse{
		Body: &alidns20150109.DescribeDomainRecordsResponseBody{
			DomainRecords: &alidns20150109.DescribeDomainRecordsResponseBodyDomainRecords{
				Record: f.describeRecords,
			},
		},
	}, nil
}

func (f *fakeSDKClient) AddDomainRecord(request *alidns20150109.AddDomainRecordRequest) (_result *alidns20150109.AddDomainRecordResponse, _err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls++
	f.lastAdd = request

	if f.addErr != nil {
		return nil, f.addErr
	}

	recordId := f.addRecordId
	if recordId == "" {
		recordId = "1001"
	}
	return &alidns20150109.AddDomainRecordResponse{
		Body: &alidns20150109.AddDomainRecordResponseBody{RecordId: dara.String(recordId)},
	}, nil
}

func (f *fakeSDKClient) UpdateDomainRecord(request *alidns20150109.UpdateDomainRecordRequest) (_result *alidns20150109.UpdateDomainRecordResponse, _err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	f.lastUpdate = request

	if f.updateErr != nil {
		return nil, f.updateErr
	}

	recordId := dara.StringValue(request.RecordId)
	if recordId == "" {
		recordId = "1002"
	}
	return &alidns20150109.UpdateDomainRecordResponse{
		Body: &alidns20150109.UpdateDomainRecordResponseBody{RecordId: dara.String(recordId)},
	}, nil
}

// newTestClient 构造一个装配了 fake SDK 客户端的 alidns Client。
func newTestClient(fake *fakeSDKClient) *Client {
	return &Client{sdkClient: fake}
}

// record 便捷构造一条现有解析记录。
func record(rr, recordType, value, recordId string) *alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord {
	return &alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
		RR:       dara.String(rr),
		Type:     dara.String(recordType),
		Value:    dara.String(value),
		RecordId: dara.String(recordId),
	}
}

/* -------------------- EnsureDomainRecord 用例 -------------------- */

// 三态语义：无记录 -> 新建；值一致 -> 跳过；值不同 -> 按覆盖开关更新或返回冲突错误。
func TestEnsureDomainRecord(t *testing.T) {
	t.Run("no record -> create", func(t *testing.T) {
		fake := &fakeSDKClient{}
		client := newTestClient(fake)

		res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:  "taoxiplan.com",
			SubDomain:   "lingji",
			RecordType:  "CNAME",
			RecordValue: "a.b.qiniudns.com",
		})
		require.NoError(t, err)
		require.NotNil(t, res)
		require.True(t, res.Created)
		require.False(t, res.Updated)
		require.False(t, res.Skipped)
		require.Equal(t, "1001", res.RecordId)

		// 发出的是 AddDomainRecord，参数正确
		require.Equal(t, 1, fake.addCalls)
		require.Equal(t, 0, fake.updateCalls)
		require.Equal(t, "taoxiplan.com", dara.StringValue(fake.lastAdd.DomainName))
		require.Equal(t, "lingji", dara.StringValue(fake.lastAdd.RR))
		require.Equal(t, "CNAME", dara.StringValue(fake.lastAdd.Type))
		require.Equal(t, "a.b.qiniudns.com", dara.StringValue(fake.lastAdd.Value))
	})

	t.Run("same value -> skip", func(t *testing.T) {
		fake := &fakeSDKClient{describeRecords: []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
			record("lingji", "CNAME", "a.b.qiniudns.com", "1001"),
		}}
		client := newTestClient(fake)

		res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:  "taoxiplan.com",
			SubDomain:   "lingji",
			RecordType:  "CNAME",
			RecordValue: "a.b.qiniudns.com",
		})
		require.NoError(t, err)
		require.NotNil(t, res)
		require.True(t, res.Skipped)
		require.False(t, res.Created)
		require.False(t, res.Updated)
		require.Equal(t, "1001", res.RecordId)

		// 未发出任何写入调用
		require.Equal(t, 0, fake.addCalls)
		require.Equal(t, 0, fake.updateCalls)
	})

	t.Run("different value without overwrite -> conflict error contains existing value", func(t *testing.T) {
		fake := &fakeSDKClient{describeRecords: []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
			record("lingji", "CNAME", "other.example.com", "1001"),
		}}
		client := newTestClient(fake)

		_, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:  "taoxiplan.com",
			SubDomain:   "lingji",
			RecordType:  "CNAME",
			RecordValue: "a.b.qiniudns.com",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, ErrRecordConflict)
		// 错误信息包含现有记录值
		require.Contains(t, err.Error(), "other.example.com")

		// 未发出任何写入调用
		require.Equal(t, 0, fake.addCalls)
		require.Equal(t, 0, fake.updateCalls)
	})

	t.Run("different value with overwrite -> update", func(t *testing.T) {
		fake := &fakeSDKClient{describeRecords: []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
			record("lingji", "CNAME", "other.example.com", "1001"),
		}}
		client := newTestClient(fake)

		res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:        "taoxiplan.com",
			SubDomain:         "lingji",
			RecordType:        "CNAME",
			RecordValue:       "a.b.qiniudns.com",
			OverwriteExisting: true,
		})
		require.NoError(t, err)
		require.NotNil(t, res)
		require.True(t, res.Updated)
		require.False(t, res.Created)
		require.False(t, res.Skipped)
		require.Equal(t, "1001", res.RecordId)

		// 发出的是 UpdateDomainRecord，目标是既有记录
		require.Equal(t, 0, fake.addCalls)
		require.Equal(t, 1, fake.updateCalls)
		require.Equal(t, "1001", dara.StringValue(fake.lastUpdate.RecordId))
		require.Equal(t, "lingji", dara.StringValue(fake.lastUpdate.RR))
		require.Equal(t, "a.b.qiniudns.com", dara.StringValue(fake.lastUpdate.Value))
	})
}

// 记录值大小写必须原样保留：比较与写入均不得改变大小写。
func TestEnsureDomainRecordValueCasePreserved(t *testing.T) {
	t.Run("same value with mixed case -> skip", func(t *testing.T) {
		fake := &fakeSDKClient{describeRecords: []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
			record("lingji", "CNAME", "CDN-Target.QiniuDns.COM", "1001"),
		}}
		client := newTestClient(fake)

		// 记录值比较不改变大小写（与既有记录完全一致的混合大小写值应判定为一致）
		res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:  "taoxiplan.com",
			SubDomain:   "lingji",
			RecordType:  "CNAME",
			RecordValue: "CDN-Target.QiniuDns.COM",
		})
		require.NoError(t, err)
		require.True(t, res.Skipped)
		require.Equal(t, 0, fake.addCalls)
		require.Equal(t, 0, fake.updateCalls)
	})

	t.Run("created record value keeps original case", func(t *testing.T) {
		fake := &fakeSDKClient{}
		client := newTestClient(fake)

		res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
			MainDomain:  "taoxiplan.com",
			SubDomain:   "lingji",
			RecordType:  "CNAME",
			RecordValue: "CDN-Target.QiniuDns.COM",
		})
		require.NoError(t, err)
		require.True(t, res.Created)
		// 写入的记录值保持原样大小写
		require.Equal(t, "CDN-Target.QiniuDns.COM", dara.StringValue(fake.lastAdd.Value))
	})
}

// 主机记录（RR）比较忽略大小写（域名不区分大小写）。
func TestEnsureDomainRecordRRIgnoreCase(t *testing.T) {
	fake := &fakeSDKClient{describeRecords: []*alidns20150109.DescribeDomainRecordsResponseBodyDomainRecordsRecord{
		record("LingJi", "CNAME", "a.b.qiniudns.com", "1001"),
	}}
	client := newTestClient(fake)

	// 既有记录 RR 为 "LingJi"，请求 SubDomain 为 "lingji"，应判定为同一条记录（跳过而非重复创建）
	res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
		MainDomain:  "taoxiplan.com",
		SubDomain:   "lingji",
		RecordType:  "CNAME",
		RecordValue: "a.b.qiniudns.com",
	})
	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.Equal(t, "1001", res.RecordId)
	require.Equal(t, 0, fake.addCalls)
	require.Equal(t, 0, fake.updateCalls)
}

// SubDomain 为空串表示根记录，内部转 "@"。
func TestEnsureDomainRecordEmptySubDomainToAt(t *testing.T) {
	fake := &fakeSDKClient{}
	client := newTestClient(fake)

	res, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
		MainDomain:  "taoxiplan.com",
		SubDomain:   "",
		RecordType:  "CNAME",
		RecordValue: "a.b.qiniudns.com",
	})
	require.NoError(t, err)
	require.True(t, res.Created)
	require.NotEmpty(t, res.RecordId)

	// 查询与新建的主机记录均为 "@"
	require.Equal(t, "@", dara.StringValue(fake.lastDescribe.RRKeyWord))
	require.Equal(t, "@", dara.StringValue(fake.lastAdd.RR))
}

// 参数缺失时快速报错，不发出任何 SDK 调用。
func TestEnsureDomainRecordInvalidRequest(t *testing.T) {
	fake := &fakeSDKClient{}
	client := newTestClient(fake)

	_, err := client.EnsureDomainRecord(context.Background(), nil)
	require.Error(t, err)

	_, err = client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{RecordType: "CNAME"})
	require.Error(t, err)

	_, err = client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{MainDomain: "taoxiplan.com"})
	require.Error(t, err)

	require.Equal(t, 0, fake.describeCalls)
	require.Equal(t, 0, fake.addCalls)
}

// 平台错误原样透出（%w 包装）。
func TestEnsureDomainRecordSDKError(t *testing.T) {
	sdkErr := errors.New("InvalidAccessKeyId.NotFound : The AccessKey ID does not exist")

	fake := &fakeSDKClient{describeErr: sdkErr}
	client := newTestClient(fake)

	_, err := client.EnsureDomainRecord(context.Background(), &EnsureDomainRecordRequest{
		MainDomain:  "taoxiplan.com",
		SubDomain:   "lingji",
		RecordType:  "CNAME",
		RecordValue: "a.b.qiniudns.com",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, sdkErr)
}

/* -------------------- SplitMainDomain 用例 -------------------- */

// 拆分边界表：三级子域 / 根域名 / 尾点 / co.uk 类多级后缀 / 单标签报错 / 大写归一。
func TestSplitMainDomain(t *testing.T) {
	tests := []struct {
		name       string
		fqdn       string
		mainDomain string
		subDomain  string
		wantErr    bool
	}{
		{name: "second level sub domain", fqdn: "lingji.taoxiplan.com", mainDomain: "taoxiplan.com", subDomain: "lingji"},
		{name: "third level sub domain", fqdn: "a.b.example.com", mainDomain: "example.com", subDomain: "a.b"},
		{name: "apex domain", fqdn: "taoxiplan.com", mainDomain: "taoxiplan.com", subDomain: ""},
		{name: "trailing dot", fqdn: "lingji.taoxiplan.com.", mainDomain: "taoxiplan.com", subDomain: "lingji"},
		{name: "trailing dot apex", fqdn: "taoxiplan.com.", mainDomain: "taoxiplan.com", subDomain: ""},
		{name: "multi-level public suffix (co.uk)", fqdn: "a.example.co.uk", mainDomain: "example.co.uk", subDomain: "a"},
		{name: "multi-level public suffix apex (co.uk)", fqdn: "example.co.uk", mainDomain: "example.co.uk", subDomain: ""},
		{name: "underscore prefixed sub domain", fqdn: "_dnsauth.lingji.xx.com", mainDomain: "xx.com", subDomain: "_dnsauth.lingji"},
		{name: "uppercase normalized", fqdn: "LINGJI.TaoxiPlan.COM", mainDomain: "taoxiplan.com", subDomain: "lingji"},
		{name: "uppercase apex normalized", fqdn: "TaoxiPlan.COM", mainDomain: "taoxiplan.com", subDomain: ""},
		{name: "single label", fqdn: "localhost", wantErr: true},
		{name: "empty string", fqdn: "", wantErr: true},
		{name: "dot only", fqdn: ".", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mainDomain, subDomain, err := SplitMainDomain(tt.fqdn)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.mainDomain, mainDomain)
			require.Equal(t, tt.subDomain, subDomain)
			// 归一化约定：主域名与主机记录均为小写
			require.Equal(t, strings.ToLower(mainDomain), mainDomain)
			require.Equal(t, strings.ToLower(subDomain), subDomain)
		})
	}
}
