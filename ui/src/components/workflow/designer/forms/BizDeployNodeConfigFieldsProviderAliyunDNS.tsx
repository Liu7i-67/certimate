import { getI18n, useTranslation } from "react-i18next";
import { Form, Input, Select, Switch } from "antd";
import { createSchemaFieldRule } from "antd-zod";
import { z } from "zod";

import { isDomain } from "@/utils/validator";

import { useFormNestedFieldsContext } from "./_context";

const RECORD_TYPE_A = "A" as const;
const RECORD_TYPE_AAAA = "AAAA" as const;
const RECORD_TYPE_CNAME = "CNAME" as const;
const RECORD_TYPE_TXT = "TXT" as const;
const RECORD_TYPE_VALUES = [RECORD_TYPE_A, RECORD_TYPE_AAAA, RECORD_TYPE_CNAME, RECORD_TYPE_TXT] as const;

const BizDeployNodeConfigFieldsProviderAliyunDNS = () => {
  const { i18n, t } = useTranslation();

  const { parentNamePath } = useFormNestedFieldsContext();
  const formSchema = z.object({
    [parentNamePath]: getSchema({ i18n }),
  });
  const formRule = createSchemaFieldRule(formSchema);
  const initialValues = getInitialValues();

  return (
    <>
      <Form.Item
        name={[parentNamePath, "domain"]}
        initialValue={initialValues.domain}
        label={t("workflow_node.deploy.form.aliyun_dns_domain.label")}
        rules={[formRule]}
      >
        <Input placeholder={t("workflow_node.deploy.form.aliyun_dns_domain.placeholder")} />
      </Form.Item>

      <Form.Item
        name={[parentNamePath, "recordType"]}
        initialValue={initialValues.recordType}
        label={t("workflow_node.deploy.form.aliyun_dns_record_type.label")}
        rules={[formRule]}
      >
        <Select
          options={RECORD_TYPE_VALUES.map((v) => ({
            label: v,
            value: v,
          }))}
          placeholder={t("workflow_node.deploy.form.aliyun_dns_record_type.placeholder")}
        />
      </Form.Item>

      <Form.Item
        name={[parentNamePath, "recordValue"]}
        initialValue={initialValues.recordValue}
        label={t("workflow_node.deploy.form.aliyun_dns_record_value.label")}
        rules={[formRule]}
        tooltip={<span dangerouslySetInnerHTML={{ __html: t("workflow_node.deploy.form.aliyun_dns_record_value.tooltip") }}></span>}
      >
        <Input placeholder={t("workflow_node.deploy.form.aliyun_dns_record_value.placeholder")} />
      </Form.Item>

      <Form.Item
        name={[parentNamePath, "overwriteExisting"]}
        initialValue={initialValues.overwriteExisting}
        label={t("workflow_node.deploy.form.aliyun_dns_overwrite_existing.label")}
        rules={[formRule]}
        tooltip={<span dangerouslySetInnerHTML={{ __html: t("workflow_node.deploy.form.aliyun_dns_overwrite_existing.tooltip") }}></span>}
      >
        <Switch />
      </Form.Item>
    </>
  );
};

const getInitialValues = (): Nullish<z.infer<ReturnType<typeof getSchema>>> => {
  return {
    domain: "",
    recordType: RECORD_TYPE_A,
    recordValue: "",
    overwriteExisting: false,
  };
};

const getSchema = ({ i18n = getI18n() }: { i18n?: ReturnType<typeof getI18n> }) => {
  const { t: _ } = i18n;

  return z.object({
    domain: z
      .string()
      .nonempty(_("workflow_node.deploy.form.aliyun_dns_domain.placeholder"))
      .refine((v) => isDomain(v), _("workflow_node.deploy.form.aliyun_dns_domain.placeholder")),
    recordType: z.enum(RECORD_TYPE_VALUES, {
      message: _("workflow_node.deploy.form.aliyun_dns_record_type.placeholder"),
    }),
    recordValue: z.string().nonempty(_("workflow_node.deploy.form.aliyun_dns_record_value.placeholder")),
    overwriteExisting: z.boolean().optional(),
  });
};

const _default = Object.assign(BizDeployNodeConfigFieldsProviderAliyunDNS, {
  getInitialValues,
  getSchema,
});

export default _default;
