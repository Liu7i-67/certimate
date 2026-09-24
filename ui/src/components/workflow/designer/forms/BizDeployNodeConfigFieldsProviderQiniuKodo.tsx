import { getI18n, useTranslation } from "react-i18next";
import { Divider, Form, Input, InputNumber, Select, Switch, Typography } from "antd";
import { createSchemaFieldRule } from "antd-zod";
import { z } from "zod";

import AccessSelect from "@/components/access/AccessSelect";
import Show from "@/components/Show";
import { type AccessModel } from "@/domain/access";
import { isDomain } from "@/utils/validator";

import { useFormNestedFieldsContext } from "./_context";

// v1 仅支持阿里云 DNS 作为 DNS 提供商
const DNS_PROVIDER_ALIYUN_DNS = "aliyun-dns" as const;

// 域名加速区域：中国大陆（需 ICP 备案）/ 全球（需 ICP 备案）/ 境外（免备案）
const GEO_COVER_CHINA = "china" as const;
const GEO_COVER_GLOBAL = "global" as const;
const GEO_COVER_FOREIGN = "foreign" as const;
const GEO_COVER_VALUES = [GEO_COVER_CHINA, GEO_COVER_GLOBAL, GEO_COVER_FOREIGN] as const;

const BizDeployNodeConfigFieldsProviderQiniuKodo = () => {
  const { i18n, t } = useTranslation();

  const { parentNamePath } = useFormNestedFieldsContext();
  const formSchema = z.object({
    [parentNamePath]: getSchema({ i18n }),
  });
  const formRule = createSchemaFieldRule(formSchema);
  const formInst = Form.useFormInstance();
  const initialValues = getInitialValues();

  const fieldAutoOnboard = Form.useWatch([parentNamePath, "autoOnboard"], { form: formInst, preserve: true });
  const fieldGeoCover = Form.useWatch([parentNamePath, "geoCover"], { form: formInst, preserve: true });

  const dnsAccessOptionFilter = (_: string, option: AccessModel) => {
    if (option.reserve) return false;
    return option.provider === "aliyun";
  };

  return (
    <>
      <Form.Item
        name={[parentNamePath, "bucket"]}
        initialValue={initialValues.bucket}
        label={t("workflow_node.deploy.form.qiniu_kodo_bucket.label")}
        rules={[formRule]}
      >
        <Input placeholder={t("workflow_node.deploy.form.qiniu_kodo_bucket.placeholder")} />
      </Form.Item>

      <Form.Item
        name={[parentNamePath, "domain"]}
        initialValue={initialValues.domain}
        label={t("workflow_node.deploy.form.qiniu_kodo_domain.label")}
        rules={[formRule]}
      >
        <Input placeholder={t("workflow_node.deploy.form.qiniu_kodo_domain.placeholder")} />
      </Form.Item>

      <Divider size="small">
        <Typography.Text className="text-xs font-normal" type="secondary">
          {t("workflow_node.deploy.form.qiniu_kodo_auto_onboard.block")}
        </Typography.Text>
      </Divider>

      <Form.Item
        name={[parentNamePath, "autoOnboard"]}
        initialValue={initialValues.autoOnboard}
        label={t("workflow_node.deploy.form.qiniu_kodo_auto_onboard.label")}
        rules={[formRule]}
        tooltip={<span dangerouslySetInnerHTML={{ __html: t("workflow_node.deploy.form.qiniu_kodo_auto_onboard.tooltip") }}></span>}
      >
        <Switch />
      </Form.Item>

      <Show when={!!fieldAutoOnboard}>
        <Form.Item
          name={[parentNamePath, "dnsProviderProvider"]}
          initialValue={initialValues.dnsProviderProvider}
          label={t("workflow_node.deploy.form.qiniu_kodo_dns_provider.label")}
          rules={[formRule]}
        >
          <Select
            options={[
              {
                label: t("workflow_node.deploy.form.qiniu_kodo_dns_provider.option.aliyun_dns.label"),
                value: DNS_PROVIDER_ALIYUN_DNS,
              },
            ]}
            placeholder={t("workflow_node.deploy.form.qiniu_kodo_dns_provider.placeholder")}
          />
        </Form.Item>

        <Form.Item
          name={[parentNamePath, "dnsProviderAccessId"]}
          initialValue={initialValues.dnsProviderAccessId}
          label={t("workflow_node.deploy.form.qiniu_kodo_dns_provider_access.label")}
          rules={[formRule]}
        >
          <AccessSelect placeholder={t("workflow_node.deploy.form.qiniu_kodo_dns_provider_access.placeholder")} showSearch onFilter={dnsAccessOptionFilter} />
        </Form.Item>

        <Form.Item
          name={[parentNamePath, "dnsOverwriteExisting"]}
          initialValue={initialValues.dnsOverwriteExisting}
          label={t("workflow_node.deploy.form.qiniu_kodo_dns_overwrite_existing.label")}
          rules={[formRule]}
          tooltip={<span dangerouslySetInnerHTML={{ __html: t("workflow_node.deploy.form.qiniu_kodo_dns_overwrite_existing.tooltip") }}></span>}
        >
          <Switch />
        </Form.Item>

        <Form.Item
          name={[parentNamePath, "waitVerifyTimeout"]}
          initialValue={initialValues.waitVerifyTimeout}
          label={t("workflow_node.deploy.form.qiniu_kodo_wait_verify_timeout.label")}
          rules={[formRule]}
          tooltip={<span dangerouslySetInnerHTML={{ __html: t("workflow_node.deploy.form.qiniu_kodo_wait_verify_timeout.tooltip") }}></span>}
        >
          <InputNumber
            className="w-full"
            min={60}
            max={3600}
            precision={0}
            placeholder={t("workflow_node.deploy.form.qiniu_kodo_wait_verify_timeout.placeholder")}
            suffix={t("workflow_node.deploy.form.qiniu_kodo_wait_verify_timeout.unit")}
          />
        </Form.Item>

        <Form.Item
          name={[parentNamePath, "geoCover"]}
          initialValue={initialValues.geoCover}
          label={t("workflow_node.deploy.form.qiniu_kodo_geo_cover.label")}
          rules={[formRule]}
        >
          <Select
            options={[
              {
                label: t("workflow_node.deploy.form.qiniu_kodo_geo_cover.option.china.label"),
                value: GEO_COVER_CHINA,
              },
              {
                label: t("workflow_node.deploy.form.qiniu_kodo_geo_cover.option.global.label"),
                value: GEO_COVER_GLOBAL,
              },
              {
                label: t("workflow_node.deploy.form.qiniu_kodo_geo_cover.option.foreign.label"),
                value: GEO_COVER_FOREIGN,
              },
            ]}
            placeholder={t("workflow_node.deploy.form.qiniu_kodo_geo_cover.placeholder")}
          />
        </Form.Item>

        <Show when={fieldGeoCover !== GEO_COVER_FOREIGN}>
          <Form.Item
            name={[parentNamePath, "icpRegisterNo"]}
            initialValue={initialValues.icpRegisterNo}
            label={t("workflow_node.deploy.form.qiniu_kodo_icp_register_no.label")}
            rules={[formRule]}
          >
            <Input placeholder={t("workflow_node.deploy.form.qiniu_kodo_icp_register_no.placeholder")} />
          </Form.Item>
        </Show>
      </Show>
    </>
  );
};

const getInitialValues = (): Nullish<z.infer<ReturnType<typeof getSchema>>> => {
  return {
    bucket: "",
    domain: "",
    autoOnboard: false,
    dnsProviderProvider: DNS_PROVIDER_ALIYUN_DNS,
    dnsProviderAccessId: "",
    dnsOverwriteExisting: false,
    waitVerifyTimeout: 600,
    geoCover: GEO_COVER_CHINA,
    icpRegisterNo: "",
  };
};

const getSchema = ({ i18n = getI18n() }: { i18n?: ReturnType<typeof getI18n> }) => {
  const { t } = i18n;

  return z
    .object({
      bucket: z.string().nonempty(),
      domain: z.string().refine((v) => isDomain(v), t("common.errmsg.domain_invalid")),
      autoOnboard: z.boolean().nullish(),
      dnsProviderProvider: z.string().nullish(),
      dnsProviderAccessId: z.string().nullish(),
      dnsOverwriteExisting: z.boolean().nullish(),
      waitVerifyTimeout: z.coerce.number().int().min(60).max(3600).nullish(),
      // 加速区域：非空时必须为三值之一（undefined/null 兼容存量工作流配置）
      geoCover: z
        .string()
        .nullish()
        .refine(
          (v) => v == null || v === "" || (GEO_COVER_VALUES as readonly string[]).includes(v),
          t("workflow_node.deploy.form.qiniu_kodo_geo_cover.errmsg.invalid")
        ),
      // ICP 备案号选填：不做硬必填校验，避免存量工作流编辑被卡，备案校验交由平台错误透出（决策记录 #19）
      icpRegisterNo: z.string().nullish(),
    })
    .superRefine((values, ctx) => {
      // 启用自动接入域名时，暂不支持泛域名
      if (values.autoOnboard && values.domain.trim().startsWith("*")) {
        ctx.addIssue({
          code: "custom",
          message: t("workflow_node.deploy.form.qiniu_kodo_auto_onboard.errmsg.wildcard_unsupported"),
          path: ["domain"],
        });
      }

      // 启用自动接入域名时，必须选择 DNS 提供商的授权
      if (values.autoOnboard && !values.dnsProviderAccessId) {
        ctx.addIssue({
          code: "custom",
          message: t("workflow_node.deploy.form.qiniu_kodo_dns_provider_access.placeholder"),
          path: ["dnsProviderAccessId"],
        });
      }
    });
};

const _default = Object.assign(BizDeployNodeConfigFieldsProviderQiniuKodo, {
  getInitialValues,
  getSchema,
});

export default _default;
