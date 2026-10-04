export class AuthSessionExpiredError extends Error {
  constructor(message = "登录态已失效，请重新登录") {
    super(message);
    this.name = "AuthSessionExpiredError";
  }
}
