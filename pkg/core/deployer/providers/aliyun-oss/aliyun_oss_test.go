package aliyunoss

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	"github.com/stretchr/testify/require"

	"github.com/certimate-go/certimate/pkg/core"
	alidns "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/alidns"
	osssdk "github.com/certimate-go/certimate/pkg/sdk3rd/alibabacloud/oss"
)

const (
	testCertPEM    = "-----BEGIN CERTIFICATE-----\nTEST-CERT\n-----END CERTIFICATE-----\n"
	testPrivkeyPEM = "-----BEGIN PRIVATE KEY-----\nTEST-KEY\n-----END PRIVATE KEY-----\n"

	testDomain     = "test.example.com"
	testMainDomain = "example.com"
	testToken      = "TOKEN123"
)

// ---------- 以下为各依赖项的 fake 实现 ----------

type fakeOssCnameClient struct {
	listFunc   func() (*osssdk.ListBucketCnameResponse, error)
	createFunc func() (*osssdk.CreateCnameTokenResponse, error)
	getFunc    func() (*osssdk.GetCnameTokenResponse, error)
	putFunc    func(req *osssdk.PutCnameRequest) error

	listCalls   int
	createCalls int
	getCalls    int
	putCalls    int

	putRequests []*osssdk.PutCnameRequest
}

func (c *fakeOssCnameClient) ListBucketCnameWithContext(ctx context.Context, req *osssdk.ListBucketCnameRequest) (*osssdk.ListBucketCnameResponse, error) {
	c.listCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.listFunc == nil {
		return &osssdk.ListBucketCnameResponse{}, nil
	}
	return c.listFunc()
}

func (c *fakeOssCnameClient) CreateCnameTokenWithContext(ctx context.Context, req *osssdk.CreateCnameTokenRequest) (*osssdk.CreateCnameTokenResponse, error) {
	c.createCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.createFunc == nil {
		return &osssdk.CreateCnameTokenResponse{}, nil
	}
	return c.createFunc()
}

func (c *fakeOssCnameClient) GetCnameTokenWithContext(ctx context.Context, cname string) (*osssdk.GetCnameTokenResponse, error) {
	c.getCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.getFunc == nil {
		return &osssdk.GetCnameTokenResponse{}, nil
	}
	return c.getFunc()
}

func (c *fakeOssCnameClient) PutBucketCnameWithContext(ctx context.Context, req *osssdk.PutCnameRequest) (*osssdk.PutCnameResponse, error) {
	c.putCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.putRequests = append(c.putRequests, req)
	if c.putFunc == nil {
		return &osssdk.PutCnameResponse{}, nil
	}
	if err := c.putFunc(req); err != nil {
		return nil, err
	}
	return &osssdk.PutCnameResponse{}, nil
}

type fakeDnsRecordClient struct {
	ensureFunc func(req *alidns.EnsureDomainRecordRequest) (*alidns.EnsureDomainRecordResult, error)

	ensureCalls int
	lastRequest *alidns.EnsureDomainRecordRequest
}

func (c *fakeDnsRecordClient) EnsureDomainRecord(ctx context.Context, req *alidns.EnsureDomainRecordRequest) (*alidns.EnsureDomainRecordResult, error) {
	c.ensureCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.lastRequest = req
	if c.ensureFunc == nil {
		return &alidns.EnsureDomainRecordResult{RecordId: "1", Created: true}, nil
	}
	return c.ensureFunc(req)
}

type fakeCertmgr struct {
	uploadCalls int
}

func (m *fakeCertmgr) SetLogger(logger *slog.Logger) {}

func (m *fakeCertmgr) Upload(ctx context.Context, certPEM, privkeyPEM string) (*core.CertmgrUploadResult, error) {
	m.uploadCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &core.CertmgrUploadResult{
		CertId:       "123456",
		ExtendedData: map[string]any{"CertIdWithRegion": "123456-cn-hangzhou"},
	}, nil
}

func (m *fakeCertmgr) Replace(ctx context.Context, certIdOrName string, certPEM, privkeyPEM string) (*core.CertmgrReplaceResult, error) {
	return &core.CertmgrReplaceResult{}, nil
}

// ---------- 以下为测试辅助 ----------

// 模拟 OSS 平台返回的错误（错误信息携带响应体原文，与 sdk3rd 封装行为一致）
func newFakeOssPlatformError(code string) error {
	return fmt.Errorf("sdkerr: unexpected status code: 403 (resp: <?xml version=\"1.0\" encoding=\"UTF-8\"?><Error><Code>%s</Code><Message>oss error: %s</Message></Error>)", code, code)
}

// 模拟 OSS 平台返回的 ServiceError（与 sdk3rd 封装对非 2xx 响应的错误构造一致）
func newFakeOssServiceError(statusCode int, code string) *osssdk.ServiceError {
	body := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?><Error><Code>%s</Code><Message>oss error: %s</Message></Error>", code, code)
	return &osssdk.ServiceError{
		StatusCode: statusCode,
		Code:       code,
		Message:    "oss error: " + code,
		Body:       body,
	}
}

func newTestDeployer(t *testing.T, config *DeployerConfig) (*Deployer, *fakeOssCnameClient, *fakeDnsRecordClient, *fakeCertmgr) {
	t.Helper()

	deployer, err := NewDeployer(config)
	require.NoError(t, err, "could not create deployer")

	fakeOss := &fakeOssCnameClient{}
	fakeDns := &fakeDnsRecordClient{}
	fakeCertmgr := &fakeCertmgr{}

	deployer.sdkClient = fakeOss
	deployer.dnsClient = fakeDns
	deployer.sdkCertmgr = fakeCertmgr

	return deployer, fakeOss, fakeDns, fakeCertmgr
}

// 配置未接入流程的 fake 行为：域名未接入 → 创建 Token → 写 TXT → PutCname 探测 once 成功
func setupFullFlowFakes(fakeOss *fakeOssCnameClient, fakeDns *fakeDnsRecordClient, putFailsBefore int) {
	fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
		return &osssdk.ListBucketCnameResponse{}, nil
	}
	fakeOss.createFunc = func() (*osssdk.CreateCnameTokenResponse, error) {
		return &osssdk.CreateCnameTokenResponse{
			Bucket:     tea.String("test-bucket"),
			Cname:      tea.String(testDomain),
			Token:      tea.String(testToken),
			ExpireTime: tea.String("Wed, 23 Feb 2022 21:16:37 GMT"),
		}, nil
	}
	fakeOss.getFunc = func() (*osssdk.GetCnameTokenResponse, error) {
		return &osssdk.GetCnameTokenResponse{
			Bucket:     tea.String("test-bucket"),
			Cname:      tea.String(testDomain),
			Token:      tea.String(testToken),
			ExpireTime: tea.String("Wed, 23 Feb 2022 21:16:37 GMT"),
		}, nil
	}
	fakeOss.putFunc = func(_ *osssdk.PutCnameRequest) error {
		if fakeOss.putCalls <= putFailsBefore {
			return newFakeOssPlatformError("NeedVerifyDomainOwnership")
		}
		return nil
	}
	fakeDns.ensureFunc = func(_ *alidns.EnsureDomainRecordRequest) (*alidns.EnsureDomainRecordResult, error) {
		return &alidns.EnsureDomainRecordResult{RecordId: "1", Created: true}, nil
	}
}

// ---------- 以下为单元测试 ----------

// S-04：autoOnboard=true 且 DNS 凭证为空时，NewDeployer 应在初始化阶段返回错误（而非等到运行时才失败）
func TestNewDeployerAutoOnboardRequiresDnsCredentials(t *testing.T) {
	t.Parallel()

	deployer, err := NewDeployer(&DeployerConfig{
		AccessKeyId:     "AK",
		AccessKeySecret: "SK",
		Region:          "cn-hangzhou",
		Bucket:          "test-bucket",
		Domain:          testDomain,
		AutoOnboard:     true,
	})
	require.Error(t, err)
	require.Nil(t, deployer)
	require.ErrorContains(t, err, "dnsAccessKeyId")
	require.ErrorContains(t, err, "dnsAccessKeySecret")
}

// T1（oss 部分）：autoOnboard=false 时与原逻辑一致，不调用任何自动接入相关方法
func TestDeployWithoutAutoOnboard(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, fakeCertmgr := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:     "AK",
		AccessKeySecret: "SK",
		Region:          "cn-hangzhou",
		Bucket:          "test-bucket",
		Domain:          testDomain,
		AutoOnboard:     false,
	})

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)

	// 未调用任何自动接入相关方法
	require.Zero(t, fakeOss.listCalls, "ListBucketCname should not be called")
	require.Zero(t, fakeOss.createCalls, "CreateCnameToken should not be called")
	require.Zero(t, fakeOss.getCalls, "GetCnameToken should not be called")
	require.Zero(t, fakeDns.ensureCalls, "EnsureDomainRecord should not be called")

	// 与原逻辑一致：上传一次证书，PutCname 一次且 Force=true
	require.Equal(t, 1, fakeCertmgr.uploadCalls)
	require.Equal(t, 1, fakeOss.putCalls)
	putReq := fakeOss.putRequests[0]
	require.NotNil(t, putReq.Cname)
	require.Equal(t, testDomain, tea.StringValue(putReq.Cname.Domain))
	require.NotNil(t, putReq.Cname.CertificateConfiguration)
	require.Equal(t, "123456-cn-hangzhou", tea.StringValue(putReq.Cname.CertificateConfiguration.CertId))
	require.True(t, tea.BoolValue(putReq.Cname.CertificateConfiguration.Force))
}

// T4：泛域名在 autoOnboard=true 时无法通过 CnameToken 完成所有权验证，应被明确拒绝
func TestDeployAutoOnboardRejectsWildcardDomain(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, fakeCertmgr := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             "*." + testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})
	setupFullFlowFakes(fakeOss, fakeDns, 0)

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.Nil(t, res)
	require.Error(t, err)
	require.ErrorContains(t, err, "wildcard")

	// 校验应在任何 SDK 调用发生之前失败
	require.Zero(t, fakeCertmgr.uploadCalls)
	require.Zero(t, fakeOss.listCalls)
	require.Zero(t, fakeOss.createCalls)
	require.Zero(t, fakeOss.getCalls)
	require.Zero(t, fakeDns.ensureCalls)
	require.Zero(t, fakeOss.putCalls)
}

// T4：泛域名在 autoOnboard=false 时不受新增校验影响（原路径行为不变）
func TestDeployWithoutAutoOnboardWildcardDomain(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, _, fakeCertmgr := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:     "AK",
		AccessKeySecret: "SK",
		Region:          "cn-hangzhou",
		Bucket:          "test-bucket",
		Domain:          "*." + testDomain,
		AutoOnboard:     false,
	})

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, fakeCertmgr.uploadCalls)
	require.Equal(t, 1, fakeOss.putCalls)
	require.Zero(t, fakeOss.listCalls)
}

// T4：未接入 → 完整 CnameToken + TXT + 轮询 + PutCname 链路
func TestDeployAutoOnboardFullFlowWhenNotOnboarded(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})
	deployer.ownershipVerifyPollInterval = 10 * time.Millisecond
	setupFullFlowFakes(fakeOss, fakeDns, 2)

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)

	// CnameToken 恰好创建一次
	require.Equal(t, 1, fakeOss.createCalls)

	// TXT 记录恰好写入一次，参数正确（固定 OverwriteExisting=true）
	require.Equal(t, 1, fakeDns.ensureCalls)
	ensureReq := fakeDns.lastRequest
	require.Equal(t, testMainDomain, ensureReq.MainDomain)
	require.Equal(t, "_dnsauth.test", ensureReq.SubDomain)
	require.Equal(t, "TXT", ensureReq.RecordType)
	require.Equal(t, testToken, ensureReq.RecordValue)
	require.True(t, ensureReq.OverwriteExisting)

	// 轮询探测两次未完成验证后第三次绑定成功
	require.Positive(t, fakeOss.getCalls)
	require.Equal(t, 3, fakeOss.putCalls)
	putReq := fakeOss.putRequests[len(fakeOss.putRequests)-1]
	require.NotNil(t, putReq.Cname.CertificateConfiguration)
	require.True(t, tea.BoolValue(putReq.Cname.CertificateConfiguration.Force))
}

// T4：已接入 → 直接 PutCname，不再走 CnameToken / TXT / 轮询
func TestDeployAutoOnboardWhenAlreadyOnboarded(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})
	fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
		return &osssdk.ListBucketCnameResponse{
			Cnames: []osssdk.ListBucketCnameResponseCname{
				{Domain: tea.String(testDomain), Status: tea.String("Enabled")},
			},
		}, nil
	}

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Equal(t, 1, fakeOss.listCalls)
	require.Zero(t, fakeOss.createCalls, "CreateCnameToken should not be called")
	require.Zero(t, fakeOss.getCalls, "GetCnameToken should not be called")
	require.Zero(t, fakeDns.ensureCalls, "EnsureDomainRecord should not be called")
	require.Equal(t, 1, fakeOss.putCalls)

	putReq := fakeOss.putRequests[0]
	require.NotNil(t, putReq.Cname.CertificateConfiguration)
	require.True(t, tea.BoolValue(putReq.Cname.CertificateConfiguration.Force))
}

// T5：WaitVerifyTimeout 到期报超时错误
func TestDeployAutoOnboardWaitVerifyTimeout(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
		WaitVerifyTimeout:  1, // 1 秒即超时
	})
	deployer.ownershipVerifyPollInterval = 10 * time.Millisecond
	setupFullFlowFakes(fakeOss, fakeDns, 1<<30) // PutCname 始终返回验证未完成

	_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.Error(t, err)
	require.ErrorContains(t, err, "not completed within 1 seconds")
	require.ErrorContains(t, err, "NeedVerifyDomainOwnership")
	require.Positive(t, fakeOss.getCalls)
}

// T5：ctx 取消立即返回
func TestDeployAutoOnboardContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})
	deployer.ownershipVerifyPollInterval = 10 * time.Millisecond // 轮询间隔足够短，确保超时判定不会先于取消判定
	setupFullFlowFakes(fakeOss, fakeDns, 1<<30)
	fakeOss.putFunc = func(_ *osssdk.PutCnameRequest) error {
		cancel() // 模拟首次探测期间任务被取消
		return newFakeOssPlatformError("NeedVerifyDomainOwnership")
	}

	startAt := time.Now()
	_, err := deployer.Deploy(ctx, testCertPEM, testPrivkeyPEM)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(startAt), time.Second, "should return immediately on context cancellation")
}

// T6：平台错误完整上抛（备案/审核类错误信息不丢失）
func TestDeployAutoOnboardPlatformErrorPropagation(t *testing.T) {
	t.Parallel()

	t.Run("CreateCnameToken error", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, _, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		fakeOss.createFunc = func() (*osssdk.CreateCnameTokenResponse, error) {
			// NoSuchCnameInRecord 表示域名不在 Bucket 的 Cname 记录中，属平台错误（而非 404 空态），应原样上抛
			return nil, newFakeOssPlatformError("NoSuchCnameInRecord")
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.ErrorContains(t, err, "NoSuchCnameInRecord")
	})

	t.Run("PutCname non-retryable error", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		setupFullFlowFakes(fakeOss, fakeDns, 1<<30)
		fakeOss.putFunc = func(_ *osssdk.PutCnameRequest) error {
			return newFakeOssPlatformError("CnameIsRisk") // 非"验证未完成"类错误应立即失败
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.ErrorContains(t, err, "CnameIsRisk")
		require.Equal(t, 1, fakeOss.putCalls, "should not retry on non-retryable error")
	})

	t.Run("EnsureDomainRecord error", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		setupFullFlowFakes(fakeOss, fakeDns, 0)
		fakeDns.ensureFunc = func(_ *alidns.EnsureDomainRecordRequest) (*alidns.EnsureDomainRecordResult, error) {
			return nil, fmt.Errorf("sdkerr: InvalidAccessKeyId.NotFound")
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.ErrorContains(t, err, "InvalidAccessKeyId.NotFound")
	})
}

// T4 / UT-T4-03：ListBucketCname 返回 404 NoSuchCname（Bucket 从未绑定过任何 Cname 的空态）
// → 视为「未接入」继续走完整 CnameToken 链路，而非失败
func TestDeployAutoOnboardListBucketCname404EmptyState(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, _ := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})
	deployer.ownershipVerifyPollInterval = 10 * time.Millisecond
	setupFullFlowFakes(fakeOss, fakeDns, 0)
	fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
		return nil, newFakeOssServiceError(404, "NoSuchCname")
	}

	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)

	// 空态被正确识别后，走完整 CnameToken 链路
	require.Equal(t, 1, fakeOss.listCalls)
	require.Equal(t, 1, fakeOss.createCalls)
	require.Equal(t, 1, fakeDns.ensureCalls)
	require.Positive(t, fakeOss.getCalls)
	require.Equal(t, 1, fakeOss.putCalls)
}

// T4 / UT-T4-03：ListBucketCname 返回其他错误（非「NoSuchCname + 404」组合）时，应原样上抛而非视为空态
func TestDeployAutoOnboardListBucketCnameErrorPropagation(t *testing.T) {
	t.Parallel()

	t.Run("404 with other code", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, _, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
			// 404 但错误码为 NoSuchBucket（Bucket 不存在），不得误判为「未接入域名」
			return nil, newFakeOssServiceError(404, "NoSuchBucket")
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.ErrorContains(t, err, "NoSuchBucket")
		require.ErrorContains(t, err, "failed to execute sdk request 'oss.ListBucketCname'")
		require.Zero(t, fakeOss.createCalls, "CreateCnameToken should not be called")
	})

	t.Run("untyped error with 404 text", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, _, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
			// 非结构化错误即使文本中含 404 / NoSuchCname 字样，也不得凭字符串猜测判定为空态
			return nil, fmt.Errorf("sdkerr: unexpected status code: 404 (resp: <Error><Code>NoSuchCname</Code></Error>)")
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.Zero(t, fakeOss.createCalls, "CreateCnameToken should not be called")
	})

	t.Run("non-404 error", func(t *testing.T) {
		t.Parallel()

		deployer, fakeOss, _, _ := newTestDeployer(t, &DeployerConfig{
			AccessKeyId:        "AK",
			AccessKeySecret:    "SK",
			Region:             "cn-hangzhou",
			Bucket:             "test-bucket",
			Domain:             testDomain,
			AutoOnboard:        true,
			DnsAccessKeyId:     "DNS-AK",
			DnsAccessKeySecret: "DNS-SK",
		})
		fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
			return nil, newFakeOssServiceError(403, "AccessDenied")
		}

		_, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
		require.Error(t, err)
		require.ErrorContains(t, err, "AccessDenied")
		require.Zero(t, fakeOss.createCalls, "CreateCnameToken should not be called")
	})
}

// T7（oss 部分）：同一部署连续执行两次，无重复的 CnameToken / DNS 记录调用
func TestDeployAutoOnboardIdempotent(t *testing.T) {
	t.Parallel()

	deployer, fakeOss, fakeDns, fakeCertmgr := newTestDeployer(t, &DeployerConfig{
		AccessKeyId:        "AK",
		AccessKeySecret:    "SK",
		Region:             "cn-hangzhou",
		Bucket:             "test-bucket",
		Domain:             testDomain,
		AutoOnboard:        true,
		DnsAccessKeyId:     "DNS-AK",
		DnsAccessKeySecret: "DNS-SK",
	})

	// 模拟平台状态：创建 CnameToken 后，域名视为已接入
	onboarded := false
	fakeOss.listFunc = func() (*osssdk.ListBucketCnameResponse, error) {
		if onboarded {
			return &osssdk.ListBucketCnameResponse{
				Cnames: []osssdk.ListBucketCnameResponseCname{
					{Domain: tea.String(testDomain), Status: tea.String("Enabled")},
				},
			}, nil
		}
		return &osssdk.ListBucketCnameResponse{}, nil
	}
	fakeOss.createFunc = func() (*osssdk.CreateCnameTokenResponse, error) {
		onboarded = true
		return &osssdk.CreateCnameTokenResponse{Token: tea.String(testToken)}, nil
	}

	// 第一次部署：完整链路
	res, err := deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, fakeOss.listCalls)
	require.Equal(t, 1, fakeOss.createCalls)
	require.Equal(t, 1, fakeOss.getCalls)
	require.Equal(t, 1, fakeDns.ensureCalls)
	require.Equal(t, 1, fakeOss.putCalls)

	// 第二次部署：已接入，直接绑定证书，无重复创建/写入
	res, err = deployer.Deploy(context.Background(), testCertPEM, testPrivkeyPEM)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 2, fakeOss.listCalls)
	require.Equal(t, 1, fakeOss.createCalls, "CreateCnameToken should not be called twice")
	require.Equal(t, 1, fakeOss.getCalls, "GetCnameToken should not be called twice")
	require.Equal(t, 1, fakeDns.ensureCalls, "EnsureDomainRecord should not be called twice")
	require.Equal(t, 2, fakeOss.putCalls)
	require.Equal(t, 2, fakeCertmgr.uploadCalls)
}
