package qiniukodo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/certimate-go/certimate/pkg/core"
	alidnssdk "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/alidns"
	qiniusdk "github.com/certimate-go/certimate/pkg/sdk3rd/qiniu"
	"github.com/stretchr/testify/require"
)

/* -------------------- 测试缝的 fake 实现 -------------------- */

// fakeCertmgr：证书上传客户端。
type fakeCertmgr struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeCertmgr) SetLogger(_ *slog.Logger) {}

func (f *fakeCertmgr) Upload(_ context.Context, _, _ string) (*core.CertmgrUploadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return &core.CertmgrUploadResult{CertId: fmt.Sprintf("cert-%d", f.calls)}, nil
}

func (f *fakeCertmgr) Replace(_ context.Context, _ string, _, _ string) (*core.CertmgrReplaceResult, error) {
	return &core.CertmgrReplaceResult{}, nil
}

// fakeKodoClient：七牛 Kodo 管理客户端。
type fakeKodoClient struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeKodoClient) BindBucketCert(_ context.Context, _ string, _ string) (*qiniusdk.BindBucketCertResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return &qiniusdk.BindBucketCertResponse{}, nil
}

// fakeDomainItem：fake 域名客户端中维护的域名状态。
type fakeDomainItem struct {
	cname string
	state string
	desc  string // 操作状态描述（OperatingStateDesc）

	gets         int // 累计查询次数
	cnameReadyAt int // 第 N 次查询后 CName 生成
	stateReadyAt int // 第 N 次查询后状态变为 success
}

// fakeEnableHttpsCall：一次 EnableDomainHttps（sslize）调用的参数记录。
type fakeEnableHttpsCall struct {
	domain      string
	certId      string
	forceHttps  bool
	http2Enable bool
}

// fakeDomainClient：七牛域名管理客户端。
type fakeDomainClient struct {
	mu      sync.Mutex
	domains map[string]*fakeDomainItem

	createCalls      int
	getCalls         int
	enableHttpsCalls []fakeEnableHttpsCall         // 累计 EnableDomainHttps（sslize）调用记录
	lastCreateReq    *qiniusdk.CreateDomainRequest // 最近一次 CreateDomain 请求（供断言缓存配置等）

	createErr           error // CreateDomain 返回的错误（模拟平台错误，如备案校验失败；域名不会创建成功）
	createErrButCreated error // CreateDomain 返回的错误，但域名实际已创建成功（模拟平台瞬态误判/上次执行半途创建成功）

	verifyInfo      *qiniusdk.GetDomainVerifyInfoResponse // GetDomainVerifyInfo 的返回；nil 时返回默认"已通过验证"
	verifyInfoErr   error                                 // GetDomainVerifyInfo 返回的错误（优先于 verifyInfo，模拟端点不适用等）
	verifyInfoCalls int                                   // GetDomainVerifyInfo 累计调用次数

	checkCalls    int // CheckDomainVerify 累计调用次数
	checkErrTimes int // CheckDomainVerify 前 N 次返回错误（模拟平台侧 DNS 查询未生效），之后成功
}

func newFakeDomainClient() *fakeDomainClient {
	return &fakeDomainClient{domains: make(map[string]*fakeDomainItem)}
}

// seedDomain 预置一个已存在的域名。
func (f *fakeDomainClient) seedDomain(name, cname, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains[name] = &fakeDomainItem{cname: cname, state: state}
}

func (f *fakeDomainClient) GetDomainInfo(_ context.Context, domain string) (*qiniusdk.GetDomainInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++

	item, exists := f.domains[domain]
	if !exists {
		return nil, errors.New("no such domain")
	}

	item.gets++
	if item.cnameReadyAt > 0 && item.gets >= item.cnameReadyAt && item.cname == "" {
		item.cname = "cdn-" + strings.ReplaceAll(domain, ".", "-") + ".qiniudns.com"
	}
	if item.stateReadyAt > 0 && item.gets >= item.stateReadyAt && item.state != domainOperatingStateSuccess {
		item.state = domainOperatingStateSuccess
	}

	return &qiniusdk.GetDomainInfoResponse{Name: domain, CName: item.cname, OperatingState: item.state, OperatingStateDesc: item.desc}, nil
}

func (f *fakeDomainClient) CreateDomain(_ context.Context, req *qiniusdk.CreateDomainRequest) (*qiniusdk.CreateDomainResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreateReq = req

	// 域名实际已创建成功，但接口返回错误（模拟平台瞬态误判）
	if f.createErrButCreated != nil {
		f.domains[req.Name] = &fakeDomainItem{cnameReadyAt: 1, stateReadyAt: 2}
		return nil, f.createErrButCreated
	}

	if f.createErr != nil {
		return nil, f.createErr
	}

	// 模拟域名创建成功：CName 与生效状态稍后（再次查询时）才就绪
	f.domains[req.Name] = &fakeDomainItem{cnameReadyAt: 1, stateReadyAt: 2}
	return &qiniusdk.CreateDomainResponse{}, nil
}

func (f *fakeDomainClient) EnableDomainHttps(_ context.Context, domain string, certId string, forceHttps bool, http2Enable bool) (*qiniusdk.EnableDomainHttpsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableHttpsCalls = append(f.enableHttpsCalls, fakeEnableHttpsCall{domain: domain, certId: certId, forceHttps: forceHttps, http2Enable: http2Enable})
	return &qiniusdk.EnableDomainHttpsResponse{}, nil
}

// enableHttpsCallCount 返回 EnableDomainHttps（sslize）累计调用次数。
func (f *fakeDomainClient) enableHttpsCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.enableHttpsCalls)
}

// lastEnableHttpsCall 返回最近一次 EnableDomainHttps（sslize）调用记录。
func (f *fakeDomainClient) lastEnableHttpsCall() fakeEnableHttpsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enableHttpsCalls[len(f.enableHttpsCalls)-1]
}

func (f *fakeDomainClient) GetDomainVerifyInfo(_ context.Context, _ string) (*qiniusdk.GetDomainVerifyInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyInfoCalls++

	if f.verifyInfoErr != nil {
		return nil, f.verifyInfoErr
	}
	// 未配置时默认视为已通过验证（跳过归属权验证子流程，与存量用例行为兼容）
	if f.verifyInfo == nil {
		return &qiniusdk.GetDomainVerifyInfoResponse{State: domainVerifyStateSuccess}, nil
	}
	return f.verifyInfo, nil
}

func (f *fakeDomainClient) CheckDomainVerify(_ context.Context, _ string) (*qiniusdk.CheckDomainVerifyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls++

	// 前 N 次返回错误（模拟平台主动查询 DNS 时记录尚未生效）
	if f.checkErrTimes > 0 {
		f.checkErrTimes--
		return nil, errors.New("code(400932) : domain is not verified : ownership verification not passed yet")
	}
	return &qiniusdk.CheckDomainVerifyResponse{}, nil
}

// verifyInfoCallCount 返回 GetDomainVerifyInfo 累计调用次数。
func (f *fakeDomainClient) verifyInfoCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verifyInfoCalls
}

// checkCallCount 返回 CheckDomainVerify 累计调用次数。
func (f *fakeDomainClient) checkCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkCalls
}

// fakeDNSClient：DNS 解析记录客户端（内存存储，模拟 EnsureDomainRecord 三态语义）。
type fakeDNSClient struct {
	mu      sync.Mutex
	records map[string]string // key: "<RR>|<Type>" -> value

	createCalls int
	updateCalls int

	txtCalls int                                  // TXT 类型记录的累计写入（创建/更新/冲突）次数
	lastTXT  *alidnssdk.EnsureDomainRecordRequest // 最近一次 TXT 类型记录写入请求（供断言 main/sub/value 等）
}

func newFakeDNSClient() *fakeDNSClient {
	return &fakeDNSClient{records: make(map[string]string)}
}

func (f *fakeDNSClient) EnsureDomainRecord(_ context.Context, req *alidnssdk.EnsureDomainRecordRequest) (*alidnssdk.EnsureDomainRecordResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.RecordType == "TXT" {
		f.txtCalls++
		reqCopy := *req
		f.lastTXT = &reqCopy
	}

	key := req.SubDomain + "|" + req.RecordType
	existing, exists := f.records[key]
	if !exists {
		f.createCalls++
		f.records[key] = req.RecordValue
		return &alidnssdk.EnsureDomainRecordResult{RecordId: "rec-1", Created: true}, nil
	}

	if existing == req.RecordValue {
		return &alidnssdk.EnsureDomainRecordResult{RecordId: "rec-1", Skipped: true}, nil
	}

	if !req.OverwriteExisting {
		return nil, fmt.Errorf("%w: domain '%s', RR '%s', type '%s', existing value '%s'", alidnssdk.ErrRecordConflict, req.MainDomain, req.SubDomain, req.RecordType, existing)
	}

	f.updateCalls++
	f.records[key] = req.RecordValue
	return &alidnssdk.EnsureDomainRecordResult{RecordId: "rec-1", Updated: true}, nil
}

// txtCallCount 返回 TXT 类型记录的累计写入次数。
func (f *fakeDNSClient) txtCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txtCalls
}

// lastTXTRequest 返回最近一次 TXT 类型记录写入请求。
func (f *fakeDNSClient) lastTXTRequest() *alidnssdk.EnsureDomainRecordRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTXT
}

/* -------------------- 测试辅助 -------------------- */

const (
	testDomain = "lingji.taoxiplan.com"
	testBucket = "test-bucket"

	// 归属权验证挑战信息（模拟 verify/info 响应）。
	testVerifyHost  = "verification"      // 挑战主机记录（非固定值）
	testMainDomain  = "taoxiplan.com"     // TXT 挂载基准域（可能是根域名，与部署域名不同）
	testVerifyValue = "verify_d8f6abc123" // TXT 记录值
)

// newDoingVerifyInfo 构造一份"待验证"状态的归属权验证信息：
// TXT 挂载基准域为根域名 testMainDomain，完整记录 FQDN 为 testVerifyHost + "." + testMainDomain。
func newDoingVerifyInfo() *qiniusdk.GetDomainVerifyInfoResponse {
	return &qiniusdk.GetDomainVerifyInfoResponse{
		State:  domainVerifyStateDoing,
		Domain: testMainDomain,
		Dns: &struct {
			Host        string `json:"host"`
			RecordType  string `json:"recordType"`
			RecordValue string `json:"recordValue"`
		}{Host: testVerifyHost, RecordType: "TXT", RecordValue: testVerifyValue},
	}
}

func newTestDeployer(config *DeployerConfig, certmgr *fakeCertmgr, kodo *fakeKodoClient, domain *fakeDomainClient, dns *fakeDNSClient) *Deployer {
	return &Deployer{
		config:       config,
		logger:       slog.New(slog.DiscardHandler),
		sdkClient:    kodo,
		sdkCertmgr:   certmgr,
		sdkDomain:    domain,
		sdkDNS:       dns,
		pollInterval: 20 * time.Millisecond,
	}
}

/* -------------------- 用例 -------------------- */

// T1：autoOnboard=false 时 Deploy 路径与原逻辑一致，不调用任何新增 SDK 方法。
func TestDeploy_AutoOnboardDisabled(t *testing.T) {
	certmgr := &fakeCertmgr{}
	kodo := &fakeKodoClient{}
	domainClient := newFakeDomainClient()
	dnsClient := newFakeDNSClient()

	deployer := newTestDeployer(&DeployerConfig{
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    testBucket,
		Domain:    testDomain,
	}, certmgr, kodo, domainClient, dnsClient)

	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 1, certmgr.calls)
	require.Equal(t, 1, kodo.calls)
	require.Equal(t, 0, domainClient.enableHttpsCallCount())
	require.Equal(t, 0, domainClient.getCalls)
	require.Equal(t, 0, domainClient.createCalls)
	require.Equal(t, 0, dnsClient.createCalls)
	require.Equal(t, 0, dnsClient.updateCalls)
}

// T1-2：证书绑定按 autoOnboard 分支——开启时走 CDN EnableDomainHttps（sslize，恰一次、参数正确）且不调用 kodo.BindCert；
// 关闭时仍走 kodo.BindCert（原有路径回归）。
func TestDeploy_CertBindingBranch(t *testing.T) {
	t.Run("auto onboard enabled binds via cdn EnableDomainHttps", func(t *testing.T) {
		certmgr := &fakeCertmgr{}
		kodo := &fakeKodoClient{}
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, certmgr, kodo, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// CDN EnableDomainHttps（sslize）恰一次：参数为部署域名、上传所得证书 ID、不强制 HTTPS 跳转、不开启 HTTP/2
		require.Equal(t, 1, domainClient.enableHttpsCallCount())
		call := domainClient.lastEnableHttpsCall()
		require.Equal(t, testDomain, call.domain)
		require.Equal(t, "cert-1", call.certId)
		require.False(t, call.forceHttps)
		require.False(t, call.http2Enable)

		// 不再走 kodo.BindCert
		require.Equal(t, 0, kodo.calls)
	})

	t.Run("auto onboard disabled binds via kodo.BindCert", func(t *testing.T) {
		certmgr := &fakeCertmgr{}
		kodo := &fakeKodoClient{}
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey: "ak",
			SecretKey: "sk",
			Bucket:    testBucket,
			Domain:    testDomain,
		}, certmgr, kodo, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// 原有路径回归：仍调用 kodo.BindCert，且不触碰 CDN EnableDomainHttps
		require.Equal(t, 1, kodo.calls)
		require.Equal(t, 0, domainClient.enableHttpsCallCount())
	})
}

// T2：域名不存在时 CreateDomain 恰一次；已存在时不再创建。
func TestDeploy_AutoOnboardCreateDomainIdempotent(t *testing.T) {
	t.Run("domain not exists", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		require.Equal(t, 1, domainClient.createCalls)
		require.Equal(t, 1, dnsClient.createCalls)
	})

	t.Run("domain exists", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		domainClient.seedDomain(testDomain, "cdn-exists.qiniudns.com", domainOperatingStateSuccess)
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		require.Equal(t, 0, domainClient.createCalls)
		require.Equal(t, 1, dnsClient.createCalls)
	})
}

// T2-2：CreateDomain 请求体必须携带默认缓存配置（web 等非动态平台必填，缺失时七牛返回 400/400309）——
// 断言 Cache 为全局规则（type="all"）、遵循源站（time=0/timeunit=0），且 name 与待部署域名一致。
func TestDeploy_AutoOnboardCreateDomainRequestCache(t *testing.T) {
	domainClient := newFakeDomainClient()
	dnsClient := newFakeDNSClient()

	deployer := newTestDeployer(&DeployerConfig{
		AccessKey:          "ak",
		SecretKey:          "sk",
		Bucket:             testBucket,
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "dns-ak",
		DnsAccessKeySecret: "dns-sk",
	}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 1, domainClient.createCalls)
	require.NotNil(t, domainClient.lastCreateReq)

	// 域名即资源名：请求体 name 与待部署域名一致（SDK 侧请求路径为 POST domain/{name}）
	require.Equal(t, testDomain, domainClient.lastCreateReq.Name)

	// 默认缓存配置：全局规则、遵循源站
	require.NotNil(t, domainClient.lastCreateReq.Cache)
	require.Len(t, domainClient.lastCreateReq.Cache.CacheControls, 1)
	cacheControl := domainClient.lastCreateReq.Cache.CacheControls[0]
	require.Equal(t, int64(0), cacheControl.Time)
	require.Equal(t, int64(0), cacheControl.Timeunit)
	require.Equal(t, "all", cacheControl.Type)
}

// T3：CNAME 三态——不存在则创建；指向一致则跳过；指向不同时按覆盖开关更新或报错（错误含现有记录值）。
func TestDeploy_AutoOnboardEnsureCNAME(t *testing.T) {
	build := func(dnsClient *fakeDNSClient, overwrite bool) *Deployer {
		domainClient := newFakeDomainClient()
		domainClient.seedDomain(testDomain, "cdn-exists.qiniudns.com", domainOperatingStateSuccess)
		return newTestDeployer(&DeployerConfig{
			AccessKey:            "ak",
			SecretKey:            "sk",
			Bucket:               testBucket,
			Domain:               testDomain,
			AutoOnboard:          true,
			DnsAccessKeyId:       "dns-ak",
			DnsAccessKeySecret:   "dns-sk",
			DnsOverwriteExisting: overwrite,
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)
	}

	t.Run("record not exists", func(t *testing.T) {
		dnsClient := newFakeDNSClient()

		_, err := build(dnsClient, false).Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		require.Equal(t, 1, dnsClient.createCalls)
	})

	t.Run("record value same", func(t *testing.T) {
		dnsClient := newFakeDNSClient()
		dnsClient.records["lingji|CNAME"] = "cdn-exists.qiniudns.com"

		_, err := build(dnsClient, false).Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		require.Equal(t, 0, dnsClient.createCalls)
		require.Equal(t, 0, dnsClient.updateCalls)
	})

	t.Run("record conflict without overwrite", func(t *testing.T) {
		dnsClient := newFakeDNSClient()
		dnsClient.records["lingji|CNAME"] = "other-target.example.com"

		_, err := build(dnsClient, false).Deploy(context.Background(), "CERT", "KEY")
		require.Error(t, err)
		require.ErrorIs(t, err, alidnssdk.ErrRecordConflict)
		require.Contains(t, err.Error(), "other-target.example.com")
	})

	t.Run("record conflict with overwrite", func(t *testing.T) {
		dnsClient := newFakeDNSClient()
		dnsClient.records["lingji|CNAME"] = "other-target.example.com"

		_, err := build(dnsClient, true).Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		require.Equal(t, 1, dnsClient.updateCalls)
		require.Equal(t, "cdn-exists.qiniudns.com", dnsClient.records["lingji|CNAME"])
	})
}

// T5：等待生效超时报错；ctx 取消立即返回。
func TestDeploy_AutoOnboardWaitVerify(t *testing.T) {
	build := func(waitVerifyTimeout int32, stateReadyAt int) (*Deployer, *fakeDomainClient) {
		domainClient := newFakeDomainClient()
		domainClient.seedDomain(testDomain, "cdn-exists.qiniudns.com", "processing")
		if stateReadyAt <= 0 {
			stateReadyAt = 1 << 30 // 永不生效
		} else {
			stateReadyAt = stateReadyAt + 1
		}
		domainClient.domains[testDomain].stateReadyAt = stateReadyAt

		dnsClient := newFakeDNSClient()
		return newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
			WaitVerifyTimeout:  waitVerifyTimeout,
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient), domainClient
	}

	t.Run("timeout", func(t *testing.T) {
		deployer, _ := build(1, 0)

		startedAt := time.Now()
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.Error(t, err)
		require.Contains(t, err.Error(), "timed out")
		require.Less(t, time.Since(startedAt), 5*time.Second)
	})

	t.Run("context canceled", func(t *testing.T) {
		deployer, _ := build(600, 0)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		startedAt := time.Now()
		_, err := deployer.Deploy(ctx, "CERT", "KEY")
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		require.Less(t, time.Since(startedAt), 5*time.Second)
	})

	t.Run("effective in time", func(t *testing.T) {
		deployer, _ := build(600, 2)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
	})
}

// T6：平台错误（备案/审核类）原样透出。
func TestDeploy_AutoOnboardPlatformError(t *testing.T) {
	errFiling := errors.New("code(400020) : domain is not filed : 域名未备案")

	domainClient := newFakeDomainClient()
	domainClient.createErr = errFiling

	deployer := newTestDeployer(&DeployerConfig{
		AccessKey:          "ak",
		SecretKey:          "sk",
		Bucket:             testBucket,
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "dns-ak",
		DnsAccessKeySecret: "dns-sk",
	}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, newFakeDNSClient())

	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.Error(t, err)
	require.ErrorIs(t, err, errFiling)
	require.Contains(t, err.Error(), "域名未备案")
}

// T7：同一部署连续执行两次，无重复创建域名、无重复 DNS 记录调用。
func TestDeploy_AutoOnboardIdempotentRerun(t *testing.T) {
	certmgr := &fakeCertmgr{}
	kodo := &fakeKodoClient{}
	domainClient := newFakeDomainClient()
	dnsClient := newFakeDNSClient()

	config := &DeployerConfig{
		AccessKey:          "ak",
		SecretKey:          "sk",
		Bucket:             testBucket,
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "dns-ak",
		DnsAccessKeySecret: "dns-sk",
	}

	deployer := newTestDeployer(config, certmgr, kodo, domainClient, dnsClient)
	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	deployer2 := newTestDeployer(config, certmgr, kodo, domainClient, dnsClient)
	_, err = deployer2.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 1, domainClient.createCalls)
	require.Equal(t, 1, dnsClient.createCalls)
	require.Equal(t, 0, dnsClient.updateCalls)
	// 两次部署均通过 CDN EnableDomainHttps（sslize）绑定证书，不走 kodo.BindCert
	require.Equal(t, 0, kodo.calls)
	require.Equal(t, 2, domainClient.enableHttpsCallCount())
}

// P1-2：CreateDomain 报错时重查域名信息——重查成功视为幂等成功；重查失败透出原始 CreateDomain 错误。
func TestDeploy_AutoOnboardCreateDomainRetryIdempotent(t *testing.T) {
	t.Run("create failed but domain actually exists (recheck success)", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		// CreateDomain 返回错误但域名实际已创建成功（模拟上次执行半途创建成功或平台瞬态误判）
		domainClient.createErrButCreated = errors.New("code(500000) : internal error : mock transient failure")
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// CreateDomain 恰一次；重查成功后流程继续推进（CNAME 仍写入、证书仍上传绑定）
		require.Equal(t, 1, domainClient.createCalls)
		require.Equal(t, 1, dnsClient.createCalls)
	})

	t.Run("create failed and recheck failed (original error surfaced)", func(t *testing.T) {
		errPlatform := errors.New("code(400020) : domain is not filed : 域名未备案")

		domainClient := newFakeDomainClient()
		domainClient.createErr = errPlatform
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.Error(t, err)
		// 返回原始 CreateDomain 错误（%w 保留平台信息），而非重查错误
		require.ErrorIs(t, err, errPlatform)
		require.Contains(t, err.Error(), "cdn.CreateDomain")
		require.NotContains(t, err.Error(), "no such domain")
	})
}

// P1-3：autoOnboard=true 时拒绝泛域名；autoOnboard=false 不新增校验（保持原行为）。
func TestDeploy_WildcardDomainGuard(t *testing.T) {
	t.Run("auto onboard enabled rejects wildcard", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey:          "ak",
			SecretKey:          "sk",
			Bucket:             testBucket,
			Domain:             "*.example.com",
			AutoOnboard:        true,
			DnsAccessKeyId:     "dns-ak",
			DnsAccessKeySecret: "dns-sk",
		}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.Error(t, err)
		require.Contains(t, err.Error(), "wildcard")

		// 防线在任何 SDK 调用之前拦截
		require.Equal(t, 0, domainClient.getCalls)
		require.Equal(t, 0, domainClient.createCalls)
		require.Equal(t, 0, dnsClient.createCalls)
	})

	t.Run("auto onboard disabled keeps legacy behavior", func(t *testing.T) {
		certmgr := &fakeCertmgr{}
		kodo := &fakeKodoClient{}
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(&DeployerConfig{
			AccessKey: "ak",
			SecretKey: "sk",
			Bucket:    testBucket,
			Domain:    "*.example.com",
		}, certmgr, kodo, domainClient, dnsClient)

		// 关闭时泛域名不被新增防线拦截，走原有部署路径
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		require.Equal(t, 1, certmgr.calls)
		require.Equal(t, 1, kodo.calls)
		require.Equal(t, 0, domainClient.getCalls)
		require.Equal(t, 0, domainClient.createCalls)
	})
}

// P2-4：轮询遇到终态异常（failed/frozen/offlined）时提前终止并附平台状态描述。
func TestDeploy_AutoOnboardPollTerminalStates(t *testing.T) {
	tests := []struct {
		state string
		desc  string
	}{
		{state: domainOperatingStateFailed, desc: "审核未通过"},
		{state: domainOperatingStateFrozen, desc: "域名已被冻结"},
		{state: domainOperatingStateOfflined, desc: "域名已下线"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			domainClient := newFakeDomainClient()
			domainClient.seedDomain(testDomain, "cdn-exists.qiniudns.com", tt.state)
			domainClient.domains[testDomain].desc = tt.desc

			dnsClient := newFakeDNSClient()
			deployer := newTestDeployer(&DeployerConfig{
				AccessKey:          "ak",
				SecretKey:          "sk",
				Bucket:             testBucket,
				Domain:             testDomain,
				AutoOnboard:        true,
				DnsAccessKeyId:     "dns-ak",
				DnsAccessKeySecret: "dns-sk",
			}, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

			startedAt := time.Now()
			_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
			require.Error(t, err)

			// 错误包含状态名与平台状态描述，且立即返回（未空转到超时）
			require.Contains(t, err.Error(), fmt.Sprintf("entered %s state", tt.state))
			require.Contains(t, err.Error(), tt.desc)
			require.Less(t, time.Since(startedAt), 5*time.Second)
		})
	}
}

/* -------------------- 归属权验证（verify/info + verify/check）用例 -------------------- */

// newAutoOnboardConfig 返回开启自动接入域名的基础部署配置（用例可按需覆盖字段）。
func newAutoOnboardConfig() *DeployerConfig {
	return &DeployerConfig{
		AccessKey:          "ak",
		SecretKey:          "sk",
		Bucket:             testBucket,
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "dns-ak",
		DnsAccessKeySecret: "dns-sk",
	}
}

// V1：域名已通过验证（success）/ 无需验证（no_need）/ 无 DNS 验证挑战（Dns 为空）时，
// 直接跳过归属权验证子流程——不写 TXT、不触发校验，域名仍恰创建一次。
func TestDeploy_AutoOnboardOwnershipSkipWhenVerified(t *testing.T) {
	build := func(verifyInfo *qiniusdk.GetDomainVerifyInfoResponse) (*fakeDomainClient, *fakeDNSClient, *Deployer) {
		domainClient := newFakeDomainClient()
		domainClient.verifyInfo = verifyInfo
		dnsClient := newFakeDNSClient()
		return domainClient, dnsClient, newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)
	}

	assertSkipped := func(t *testing.T, domainClient *fakeDomainClient, dnsClient *fakeDNSClient) {
		_, err := dnsClient.records[testVerifyHost+"|TXT"]
		require.False(t, err)
		require.Equal(t, 0, dnsClient.txtCallCount())
		require.Equal(t, 0, dnsClient.updateCalls)
		require.Equal(t, 0, domainClient.checkCallCount())
		require.Equal(t, 1, domainClient.createCalls)
		// CNAME 照常写入
		require.Equal(t, 1, dnsClient.createCalls)
	}

	t.Run("state success", func(t *testing.T) {
		domainClient, dnsClient, deployer := build(&qiniusdk.GetDomainVerifyInfoResponse{State: domainVerifyStateSuccess, Domain: testMainDomain})
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		assertSkipped(t, domainClient, dnsClient)
	})

	t.Run("state no_need", func(t *testing.T) {
		domainClient, dnsClient, deployer := build(&qiniusdk.GetDomainVerifyInfoResponse{State: domainVerifyStateNoNeed, Domain: testMainDomain})
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		assertSkipped(t, domainClient, dnsClient)
	})

	t.Run("doing without dns challenge", func(t *testing.T) {
		domainClient, dnsClient, deployer := build(&qiniusdk.GetDomainVerifyInfoResponse{State: domainVerifyStateDoing, Domain: testMainDomain})
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)
		assertSkipped(t, domainClient, dnsClient)
	})
}

// V2：域名待验证（state=doing）时，按挑战信息推导 TXT 记录（挂载基准域 + 主机记录）覆盖写入，
// 轮询触发校验通过后域名恰创建一次。
func TestDeploy_AutoOnboardOwnershipVerifyFlow(t *testing.T) {
	t.Run("verify passed on first check", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		domainClient.verifyInfo = newDoingVerifyInfo()
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// TXT 按推导的主域名/主机记录/记录值写入，且固定覆盖（不受 DnsOverwriteExisting 约束）
		require.Equal(t, 1, dnsClient.txtCallCount())
		txtReq := dnsClient.lastTXTRequest()
		require.NotNil(t, txtReq)
		require.Equal(t, testMainDomain, txtReq.MainDomain)
		require.Equal(t, testVerifyHost, txtReq.SubDomain)
		require.Equal(t, "TXT", txtReq.RecordType)
		require.Equal(t, testVerifyValue, txtReq.RecordValue)
		require.True(t, txtReq.OverwriteExisting)

		// 校验触发恰一次即通过；通过后域名恰创建一次
		require.Equal(t, 1, domainClient.checkCallCount())
		require.Equal(t, 1, domainClient.createCalls)
	})

	t.Run("verify passed after retries", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		domainClient.verifyInfo = newDoingVerifyInfo()
		domainClient.checkErrTimes = 3 // 前 3 次"记录未生效"
		dnsClient := newFakeDNSClient()

		deployer := newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)
		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// 轮询重试生效：第 4 次校验通过
		require.Equal(t, 4, domainClient.checkCallCount())
		require.Equal(t, 1, domainClient.createCalls)
	})
}

// V3：校验持续不通过时按超时返回错误，错误信息含 TXT 主机记录全名与记录值（便于人工排查），且不创建域名。
func TestDeploy_AutoOnboardOwnershipCheckTimeout(t *testing.T) {
	domainClient := newFakeDomainClient()
	domainClient.verifyInfo = newDoingVerifyInfo()
	domainClient.checkErrTimes = 1 << 30 // 永不通过

	dnsClient := newFakeDNSClient()
	config := newAutoOnboardConfig()
	config.WaitVerifyTimeout = 1
	deployer := newTestDeployer(config, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

	startedAt := time.Now()
	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.Error(t, err)

	// 错误信息带上 TXT 主机记录全名（主机记录 + 挂载基准域）与记录值
	require.Contains(t, err.Error(), testVerifyHost+"."+testMainDomain)
	require.Contains(t, err.Error(), testVerifyValue)

	// 未走到创建域名；TXT 已写入、校验已轮询多次（重试生效）
	require.Equal(t, 0, domainClient.createCalls)
	require.Equal(t, 1, dnsClient.txtCallCount())
	require.Greater(t, domainClient.checkCallCount(), 1)
	require.Less(t, time.Since(startedAt), 5*time.Second)
}

// V4：verify/info 查询报错时宽容放行——不写 TXT、不触发校验，仍继续创建域名
// （真正缺失归属验证时由 CreateDomain 的平台原始错误兜底）。
func TestDeploy_AutoOnboardOwnershipInfoErrorTolerated(t *testing.T) {
	domainClient := newFakeDomainClient()
	domainClient.verifyInfoErr = errors.New("code(631307) : mock verify info unavailable")
	dnsClient := newFakeDNSClient()

	deployer := newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)
	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 0, dnsClient.txtCallCount())
	require.Equal(t, 0, domainClient.checkCallCount())
	require.Equal(t, 1, domainClient.createCalls)
}

// V5：TXT 验证记录已存在且值不同时固定覆盖更新——不受 DnsOverwriteExisting 关闭的影响
// （归属挑战记录属内部自管记录，残留旧值必然阻塞校验）。
func TestDeploy_AutoOnboardOwnershipTXTOverwrite(t *testing.T) {
	domainClient := newFakeDomainClient()
	domainClient.verifyInfo = newDoingVerifyInfo()

	dnsClient := newFakeDNSClient()
	dnsClient.records[testVerifyHost+"|TXT"] = "stale-verify-old-value"

	config := newAutoOnboardConfig()
	config.DnsOverwriteExisting = false // 显式关闭，归属 TXT 仍必须覆盖
	deployer := newTestDeployer(config, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 1, dnsClient.updateCalls)
	require.Equal(t, testVerifyValue, dnsClient.records[testVerifyHost+"|TXT"])
}

// V6：CreateDomain 请求的 GeoCover / RegisterNo 透传——配置值原样上送；GeoCover 空串取默认 china。
func TestDeploy_AutoOnboardGeoCoverAndRegisterNo(t *testing.T) {
	t.Run("explicit foreign with register no", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		config := newAutoOnboardConfig()
		config.GeoCover = "foreign"
		config.IcpRegisterNo = "京ICP备2020000001号-1"
		deployer := newTestDeployer(config, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		// foreign 免备案，RegisterNo 仍透传 config 值（是否使用由平台决定）
		require.NotNil(t, domainClient.lastCreateReq)
		require.Equal(t, "foreign", domainClient.lastCreateReq.GeoCover)
		require.Equal(t, "京ICP备2020000001号-1", domainClient.lastCreateReq.RegisterNo)
	})

	t.Run("defaults to china without register no", func(t *testing.T) {
		domainClient := newFakeDomainClient()
		dnsClient := newFakeDNSClient()

		config := newAutoOnboardConfig()
		config.GeoCover = ""
		config.IcpRegisterNo = ""
		deployer := newTestDeployer(config, &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

		_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
		require.NoError(t, err)

		require.NotNil(t, domainClient.lastCreateReq)
		require.Equal(t, "china", domainClient.lastCreateReq.GeoCover)
		require.Equal(t, "", domainClient.lastCreateReq.RegisterNo)
	})
}

// V7：NewDeployer 校验 GeoCover 取值合法性；不在此处对"china 必须有备案号"做硬校验。
func TestNewDeployer_GeoCoverValidation(t *testing.T) {
	_, err := NewDeployer(&DeployerConfig{
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    testBucket,
		Domain:    testDomain,
		GeoCover:  "unknown",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "geoCover")

	for _, geoCover := range []string{"", "china", "foreign", "global"} {
		_, err := NewDeployer(&DeployerConfig{
			AccessKey: "ak",
			SecretKey: "sk",
			Bucket:    testBucket,
			Domain:    testDomain,
			GeoCover:  geoCover,
		})
		require.NoError(t, err, "geoCover %q should be accepted", geoCover)
	}
}

// V8：归属权验证幂等回归——域名已存在时不触发任何验证调用（verify/info 与 check 均为 0 次）。
func TestDeploy_AutoOnboardOwnershipIdempotentWhenDomainExists(t *testing.T) {
	domainClient := newFakeDomainClient()
	domainClient.seedDomain(testDomain, "cdn-exists.qiniudns.com", domainOperatingStateSuccess)
	// 即使挑战处于 doing 状态，域名已存在时也不应进入验证子流程
	domainClient.verifyInfo = newDoingVerifyInfo()

	dnsClient := newFakeDNSClient()
	deployer := newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

	_, err := deployer.Deploy(context.Background(), "CERT", "KEY")
	require.NoError(t, err)

	require.Equal(t, 0, domainClient.createCalls)
	require.Equal(t, 0, domainClient.verifyInfoCallCount())
	require.Equal(t, 0, domainClient.checkCallCount())
	require.Equal(t, 0, dnsClient.txtCallCount())
}

// V9：ctx 取消时立即返回，错误链保留 context.Canceled，且错误信息含 TXT 主机记录全名与记录值。
func TestDeploy_AutoOnboardOwnershipContextCanceled(t *testing.T) {
	domainClient := newFakeDomainClient()
	domainClient.verifyInfo = newDoingVerifyInfo()
	domainClient.checkErrTimes = 1 << 30 // 校验永不通过，等待期间依赖 ctx 取消退出

	dnsClient := newFakeDNSClient()
	deployer := newTestDeployer(newAutoOnboardConfig(), &fakeCertmgr{}, &fakeKodoClient{}, domainClient, dnsClient)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	startedAt := time.Now()
	_, err := deployer.Deploy(ctx, "CERT", "KEY")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), testVerifyHost+"."+testMainDomain)
	require.Contains(t, err.Error(), testVerifyValue)
	require.Less(t, time.Since(startedAt), 5*time.Second)
}
