//go:build tester

package qiniukodo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	tester "github.com/certimate-go/certimate/pkg/core/deployer/providers-tester"
	impl "github.com/certimate-go/certimate/pkg/core/deployer/providers/qiniu-kodo"
)

var (
	fp            = tester.InitArgs("QINIUKODO_")
	fTestCertPath string
	fTestKeyPath  string
	fAccessKey    string
	fSecretKey    string
	fBucket       string
	fDomain       string
	fAutoOnboard  bool
	fDnsAccessKey string
	fDnsAccessSk  string
	fDnsOverwrite bool
	fWaitVerify   int
)

func init() {
	fp.DefineString(&fTestCertPath, "TESTCERTPATH")
	fp.DefineString(&fTestKeyPath, "TESTKEYPATH")
	fp.DefineString(&fAccessKey, "ACCESSKEY")
	fp.DefineString(&fSecretKey, "SECRETKEY")
	fp.DefineString(&fBucket, "BUCKET")
	fp.DefineString(&fDomain, "DOMAIN")
	fp.DefineBool(&fAutoOnboard, "AUTOONBOARD")
	fp.DefineString(&fDnsAccessKey, "DNSACCESSKEYID")
	fp.DefineString(&fDnsAccessSk, "DNSACCESSKEYSECRET")
	fp.DefineBool(&fDnsOverwrite, "DNSOVERWRITE")
	fp.DefineInt(&fWaitVerify, "WAITVERIFYTIMEOUT")
}

/*
Shell command to run this test:

	go test -tags=tester -v ./qiniu_kodo_test.go -args \
	--QINIUKODO_TESTCERTPATH="/path/to/your-test-cert.pem" \
	--QINIUKODO_TESTKEYPATH="/path/to/your-test-key.pem" \
	--QINIUKODO_ACCESSKEY="your-access-key" \
	--QINIUKODO_SECRETKEY="your-secret-key" \
	--QINIUKODO_BUCKET="your-bucket" \
	--QINIUKODO_DOMAIN="example.com"

To test with auto domain onboarding enabled:

	go test -tags=tester -v ./qiniu_kodo_test.go -args \
	--QINIUKODO_TESTCERTPATH="/path/to/your-test-cert.pem" \
	--QINIUKODO_TESTKEYPATH="/path/to/your-test-key.pem" \
	--QINIUKODO_ACCESSKEY="your-access-key" \
	--QINIUKODO_SECRETKEY="your-secret-key" \
	--QINIUKODO_BUCKET="your-bucket" \
	--QINIUKODO_DOMAIN="example.com" \
	--QINIUKODO_AUTOONBOARD=true \
	--QINIUKODO_DNSACCESSKEYID="your-aliyun-access-key-id" \
	--QINIUKODO_DNSACCESSKEYSECRET="your-aliyun-access-key-secret" \
	--QINIUKODO_DNSOVERWRITE=false \
	--QINIUKODO_WAITVERIFYTIMEOUT=600
*/
func TestProvider(t *testing.T) {
	fp.Parse()

	t.Run("Deploy", func(t *testing.T) {
		provider, err := impl.NewDeployer(&impl.DeployerConfig{
			AccessKey: fAccessKey,
			SecretKey: fSecretKey,
			Bucket:    fBucket,
			Domain:    fDomain,
		})
		require.NoError(t, err)

		tester.Deploy(t, provider, tester.DeployInput{CertPath: fTestCertPath, KeyPath: fTestKeyPath})
	})

	t.Run("DeployWithAutoOnboard", func(t *testing.T) {
		if !fAutoOnboard {
			t.Skip("auto onboard not enabled, skip")
		}

		provider, err := impl.NewDeployer(&impl.DeployerConfig{
			AccessKey: fAccessKey,
			SecretKey: fSecretKey,
			Bucket:    fBucket,
			Domain:    fDomain,

			AutoOnboard:          fAutoOnboard,
			DnsAccessKeyId:       fDnsAccessKey,
			DnsAccessKeySecret:   fDnsAccessSk,
			DnsOverwriteExisting: fDnsOverwrite,
			WaitVerifyTimeout:    int32(fWaitVerify),
		})
		require.NoError(t, err)

		tester.Deploy(t, provider, tester.DeployInput{CertPath: fTestCertPath, KeyPath: fTestKeyPath})
	})
}
