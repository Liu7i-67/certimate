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

// fakeDomainClient：七牛域名管理客户端。
type fakeDomainClient struct {
	mu      sync.Mutex
	domains map[string]*fakeDomainItem

	createCalls int
	getCalls    int

	createErr           error // CreateDomain 返回的错误（模拟平台错误，如备案校验失败；域名不会创建成功）
	createErrButCreated error // CreateDomain 返回的错误，但域名实际已创建成功（模拟平台瞬态误判/上次执行半途创建成功）
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

// fakeDNSClient：DNS 解析记录客户端（内存存储，模拟 EnsureDomainRecord 三态语义）。
type fakeDNSClient struct {
	mu      sync.Mutex
	records map[string]string // key: "<RR>|<Type>" -> value

	createCalls int
	updateCalls int
}

func newFakeDNSClient() *fakeDNSClient {
	return &fakeDNSClient{records: make(map[string]string)}
}

func (f *fakeDNSClient) EnsureDomainRecord(_ context.Context, req *alidnssdk.EnsureDomainRecordRequest) (*alidnssdk.EnsureDomainRecordResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

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

/* -------------------- 测试辅助 -------------------- */

const (
	testDomain = "lingji.taoxiplan.com"
	testBucket = "test-bucket"
)

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
	require.Equal(t, 0, domainClient.getCalls)
	require.Equal(t, 0, domainClient.createCalls)
	require.Equal(t, 0, dnsClient.createCalls)
	require.Equal(t, 0, dnsClient.updateCalls)
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
