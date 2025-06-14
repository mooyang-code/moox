<template>
  <div>
    <div class="login_form_box">
      <a-form :rules="rules" :model="form" layout="vertical" @submit="onSubmit">
        <a-form-item field="username" :hide-asterisk="true">
          <a-input v-model="form.username" allow-clear placeholder="请输入账号">
            <template #prefix>
              <icon-user />
            </template>
          </a-input>
        </a-form-item>
        <a-form-item field="password" :hide-asterisk="true">
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

let userStores = useUserInfoStore();
const routeStore = useRoutesConfigStore();
const router = useRouter();

// 移除写死的账号密码，提高安全性
const form = ref({
  username: "",
  password: "",
  verifyCode: "",
  remember: false
});

const rules = ref({
  username: [
    {
      required: true,
      message: "请输入账号"
    }
  ],
  password: [
    {
      required: true,
      message: "请输入密码"
    }
  ]
});

const verifyCode = ref("");
const verifyCodeChange = (code: string) => (verifyCode.value = code);

// 添加登录状态
const loginLoading = ref(false);

// 提交表单
const onSubmit = async ({ errors }: any) => {
  if (errors) return;
  onLogin();
};

// 登录
const onLogin = async () => {
  if (loginLoading.value) return;
  
  try {
    loginLoading.value = true;
    
    // 检查必填字段
    if (!form.value.username.trim()) {
      Message.error("请输入账号");
      return;
    }
    if (!form.value.password.trim()) {
      Message.error("请输入密码");
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
    
    // 检查登录是否成功
    if (res.code !== 0) {
      throw new Error(res.message || "登录失败");
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
    
    // 设置字典
    useSystemStore().setDictData();
    
    // 如果选择了记住密码，保存用户名（不保存密码）
    if (form.value.remember) {
      localStorage.setItem('remembered_username', form.value.username);
    } else {
      localStorage.removeItem('remembered_username');
    }
    
  } catch (error: any) {
    console.error('❌ 登录失败:', error);
    
    // 清理可能设置的无效token
    await userStores.setToken("");
    
    let errorMessage = "登录失败";
    if (error.message) {
      errorMessage = error.message;
    } else if (error.response) {
      errorMessage = `网络错误: ${error.response.status}`;
    }
    
    Message.error(errorMessage);
    
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
