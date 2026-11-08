<template>
  <div>
    <div class="login_form_box">
      <a-form ref="formRef" :rules="rules" :model="form" layout="vertical" @submit="onSubmit">
        <a-form-item field="username" :hide-asterisk="true" validate-trigger="input">
          <a-input v-model="form.username" allow-clear placeholder="请输入账号">
            <template #prefix>
              <icon-user />
            </template>
          </a-input>
        </a-form-item>
        <a-form-item field="password" :hide-asterisk="true" validate-trigger="input">
          <a-input-password v-model="form.password" allow-clear placeholder="请输入密码">
            <template #prefix>
              <icon-lock />
            </template>
          </a-input-password>
        </a-form-item>
        <a-form-item>
          <a-button long type="primary" html-type="submit" :loading="loginLoading">
            {{ loginLoading ? "登录中..." : "登录" }}
          </a-button>
        </a-form-item>
      </a-form>
    </div>
    <div class="ca-entry">浏览器提示证书不安全？<a class="ca-link" @click="rootCaVisible = true">下载并安装根证书</a></div>
    <root-ca-dialog v-model:visible="rootCaVisible" />
    <div class="register">Powered by Moo</div>
  </div>
</template>

<script setup lang="ts">
import { validatePasswordInput } from "./login-validation";
import { useRoute, useRouter } from "vue-router";
import { safeRedirect } from "@/utils/safe-redirect";
import { useUserInfoStore } from "@/store/modules/user-info";
import { loginAPI } from "@/api/modules/user/index";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { Message } from "@arco-design/web-vue";
import { getErrorMessage } from "@/utils/error-handler";
import RootCaDialog from "@/components/root-ca/root-ca-dialog.vue";

let userStores = useUserInfoStore();
const routeStore = useRoutesConfigStore();
const router = useRouter();
const route = useRoute();

// 表单引用
const formRef = ref();

// 移除写死的账号密码，提高安全性
const form = ref({
  username: "",
  password: ""
});

// 验证字符串格式的函数（类似后端的validateStringFormat）
const validateStringFormat = (value: string, fieldName: string): Promise<boolean> => {
  return new Promise((resolve, reject) => {
    // 检查长度
    if (value.length < 1 || value.length > 20) {
      reject(`${fieldName}长度必须在1-20个字符之间`);
      return;
    }

    // 检查字符类型（仅支持大小写字母和数字）
    const regex = /^[a-zA-Z0-9]+$/;
    if (!regex.test(value)) {
      reject(`${fieldName}只能包含大小写字母和数字`);
      return;
    }

    resolve(true);
  });
};

const rules = ref({
  username: [
    {
      required: true,
      message: "请输入账号"
    },
    {
      validator: (value: string, callback: (error?: string) => void) => {
        if (!value || value.trim().length === 0) {
          callback();
          return;
        }

        // 检查长度
        if (value.length < 1 || value.length > 20) {
          callback("账号长度必须在1-20个字符之间");
          return;
        }

        // 检查字符类型（仅支持大小写字母和数字）
        const regex = /^[a-zA-Z0-9]+$/;
        if (!regex.test(value)) {
          callback("账号只能包含大小写字母和数字");
          return;
        }

        callback();
      }
    }
  ],
  password: [
    {
      required: true,
      message: "请输入密码"
    },
    {
      validator: (value: string, callback: (error?: string) => void) => {
        if (!value || value.trim().length === 0) {
          callback();
          return;
        }

        const passwordError = validatePasswordInput(value);
        if (passwordError) {
          callback(passwordError);
          return;
        }

        callback();
      }
    }
  ]
});

// 添加登录状态
const loginLoading = ref(false);
const rootCaVisible = ref(false);

// 提交表单
const onSubmit = async ({ errors, values }: any) => {
  if (errors) {
    Message.error("请修正表单中的错误后重试");
    return;
  }

  // 额外的安全检查
  if (!values.username || !values.password) {
    Message.error("请填写完整的登录信息");
    return;
  }

  onLogin();
};

// 登录
const onLogin = async () => {
  if (loginLoading.value) return;

  try {
    loginLoading.value = true;

    // 首先进行表单验证
    const validateResult = await formRef.value?.validate();
    if (validateResult) {
      Message.error("请修正输入格式错误");
      return;
    }

    // 检查必填字段
    if (!form.value.username.trim()) {
      Message.error("请输入账号");
      return;
    }
    if (!form.value.password.trim()) {
      Message.error("请输入密码");
      return;
    }

    // 执行字符串格式验证
    try {
      await validateStringFormat(form.value.username, "账号");
      const passwordError = validatePasswordInput(form.value.password);
      if (passwordError) throw passwordError;
    } catch (error: any) {
      Message.error(error);
      return;
    }

    // 使用新的安全登录方法
    let res = await loginAPI({
      username: form.value.username,
      password: form.value.password
    });

    // 检查登录是否成功 - 使用新的ret_info协议格式
    if (res.ret_info.code !== 0) {
      throw new Error(res.ret_info.msg || "登录失败");
    }

    if (!res.access_token || !res.session_id || !res.request_signing_key || !res.expires_at) {
      throw new Error("登录响应中缺少安全会话信息");
    }

    await userStores.setLoginSession({
      token: res.access_token,
      sessionId: res.session_id,
      signingKey: res.request_signing_key,
      expiresAt: res.expires_at
    });

    // 加载用户信息
    await userStores.setAccount();

    // 加载路由信息
    await routeStore.initSetRouter();

    Message.success("登录成功");

    // 会话过期被带到登录页时回到原页面，否则回首页
    void router.replace(safeRedirect(route.query.redirect) || "/home");
  } catch (error: unknown) {
    console.error("❌ 登录失败:", error);

    // 清理可能设置的无效token
    await userStores.logOut();

    const loginErrorMessage = getErrorMessage(error, "登录失败");
    Message.error(loginErrorMessage);
  } finally {
    loginLoading.value = false;
  }
};
</script>

<style lang="scss" scoped>
.login_form_box {
  margin-top: 28px;
}
.ca-entry {
  margin-bottom: 8px;
  font-size: $font-size-body-1;
  color: $color-text-3;
  text-align: center;
  .ca-link {
    color: $color-primary;
    cursor: pointer;
  }
}
.register {
  font-size: $font-size-body-1;
  color: $color-text-3;
  text-align: center;
  cursor: pointer;
}
</style>
