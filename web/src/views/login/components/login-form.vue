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
        <a-form-item field="verifyCode" :hide-asterisk="true">
          <div class="verifyCode">
            <a-input style="width: 160px" v-model="form.verifyCode" allow-clear placeholder="请输入验证码" />
            <VerifyCode :content-height="30" :font-size-max="30" :content-width="110" @verify-code-change="verifyCodeChange" />
          </div>
        </a-form-item>
        <a-form-item field="remember">
          <div class="remember">
            <a-checkbox v-model="form.remember">记住密码</a-checkbox>
            <div class="forgot-password">忘记密码</div>
          </div>
        </a-form-item>
        <a-form-item>
          <a-button long type="primary" html-type="submit" :loading="loginLoading">
            {{ loginLoading ? '登录中...' : '登录' }}
          </a-button>
        </a-form-item>
      </a-form>
    </div>
    <div class="register">Powered by Moo</div>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from "vue-router";
import { useUserInfoStore } from "@/store/modules/user-info";
import { loginAPI } from "@/api/modules/user/index";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useSystemStore } from "@/store/modules/system";
import { Message } from "@arco-design/web-vue";
import { getErrorMessage } from "@/utils/error-handler";

let userStores = useUserInfoStore();
const routeStore = useRoutesConfigStore();
const router = useRouter();

// 表单引用
const formRef = ref();

// 移除写死的账号密码，提高安全性
const form = ref({
  username: "",
  password: "",
  verifyCode: "",
  remember: false
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
        
        // 检查长度
        if (value.length < 1 || value.length > 20) {
          callback("密码长度必须在1-20个字符之间");
          return;
        }

        // 检查字符类型（仅支持大小写字母和数字）
        const regex = /^[a-zA-Z0-9]+$/;
        if (!regex.test(value)) {
          callback("密码只能包含大小写字母和数字");
          return;
        }

        callback();
      }
    }
  ]
});

const verifyCode = ref("");
const verifyCodeChange = (code: string) => (verifyCode.value = code);

// 添加登录状态
const loginLoading = ref(false);

// 提交表单
const onSubmit = async ({ errors, values }: any) => {
  console.log('📝 表单提交验证:', { errors, values });
  
  if (errors) {
    console.log('❌ 表单验证失败:', errors);
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
      console.log('❌ 表单验证失败:', validateResult);
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
      await validateStringFormat(form.value.password, "密码");
    } catch (error: any) {
      Message.error(error);
      return;
    }
    
    console.log('🚀 开始登录...', { username: form.value.username });
    
    // 使用新的安全登录方法
    let res = await loginAPI({
      username: form.value.username,
      password: form.value.password,
      verifyCode: form.value.verifyCode
    });
    
    console.log('✅ 登录响应:', res);
    
    // 检查登录是否成功 - 使用新的ret_info协议格式
    if (res.ret_info.code !== 0) {
      throw new Error(res.ret_info.msg || "登录失败");
    }
    
    // 存储token - 适配真实后台响应格式
    if (!res.access_token) {
      throw new Error("登录响应中缺少访问令牌");
    }
    
    console.log('🔐 设置访问令牌:', res.access_token.substring(0, 20) + "...");
    await userStores.setToken(res.access_token);
    
    console.log('👤 开始获取用户信息...');
    // 加载用户信息
    await userStores.setAccount();
    
    console.log('🗂️ 开始初始化路由...');
    // 加载路由信息
    await routeStore.initSetRouter();

    Message.success("登录成功");
    
    console.log('🏠 跳转到首页...');
    // 跳转首页
    router.replace("/home");
    
    // 如果选择了记住密码，保存用户名（不保存密码）
    if (form.value.remember) {
      localStorage.setItem('remembered_username', form.value.username);
    } else {
      localStorage.removeItem('remembered_username');
    }
    
  } catch (error: unknown) {
    console.error('❌ 登录失败:', error);

    // 清理可能设置的无效token
    await userStores.setToken("");

    const loginErrorMessage = getErrorMessage(error, "登录失败");
    Message.error(loginErrorMessage);

    // 登录失败时刷新验证码
    verifyCodeChange("");
  } finally {
    loginLoading.value = false;
  }
};

// 页面加载时恢复记住的用户名
onMounted(() => {
  const rememberedUsername = localStorage.getItem('remembered_username');
  if (rememberedUsername) {
    form.value.username = rememberedUsername;
    form.value.remember = true;
  }
});
</script>

<style lang="scss" scoped>
.login_form_box {
  margin-top: 28px;
  .verifyCode {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
  }
  .remember {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    .forgot-password {
      color: $color-primary;
      cursor: pointer;
    }
  }
}
.register {
  font-size: $font-size-body-1;
  color: $color-text-3;
  text-align: center;
  cursor: pointer;
}
</style>
