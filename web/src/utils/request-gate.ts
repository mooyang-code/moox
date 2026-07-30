export class RequestGate {
  private token = 0;

  next() {
    this.token += 1;
    return this.token;
  }

  isCurrent(token: number) {
    return token === this.token;
  }
}
