package alidns

import (
	"fmt"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// 将 FQDN 拆分为主域名与主机记录（RR）。
//
// 借助 golang.org/x/net/publicsuffix 的 EffectiveTLDPlusOne 拆分；
// 入口处先将 FQDN 统一转为小写再拆分（域名本身不区分大小写）；
// fqdn 等于主域名本身时 subDomain 返回空串（调用方一般转 "@"）；
// 无法拆分时返回错误。
//
// 示例：
//
//	"lingji.taoxiplan.com"   -> ("taoxiplan.com", "lingji", nil)
//	"_dnsauth.lingji.xx.com" -> ("xx.com", "_dnsauth.lingji", nil)
//	"taoxiplan.com"          -> ("taoxiplan.com", "", nil)
//	"LINGJI.TaoxiPlan.COM"   -> ("taoxiplan.com", "lingji", nil)
func SplitMainDomain(fqdn string) (mainDomain, subDomain string, err error) {
	// 归一化：去首尾空白与结尾点号，并统一转为小写后再拆分
	fqdn = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))

	if fqdn == "" {
		return "", "", fmt.Errorf("alidns: invalid fqdn")
	}

	main, err := publicsuffix.EffectiveTLDPlusOne(fqdn)
	if err != nil {
		return "", "", fmt.Errorf("alidns: could not split fqdn '%s': %w", fqdn, err)
	}

	if main == fqdn {
		return main, "", nil
	}

	sub := strings.TrimSuffix(fqdn, "."+main)
	if sub == "" || sub == fqdn {
		return "", "", fmt.Errorf("alidns: could not split fqdn '%s'", fqdn)
	}

	return main, sub, nil
}
