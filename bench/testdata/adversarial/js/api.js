/**
 * API module with its own Config class.
 */

export class Config {
  constructor(baseURL = "/api", version = "v1", timeout = 5000) {
    this.baseURL = baseURL;
    this.version = version;
    this.timeout = timeout;
  }

  validate() {
    if (!this.baseURL.startsWith("/")) throw new Error("baseURL must start with /");
    return true;
  }

  endpoint(path) {
    return `${this.baseURL}/${this.version}/${path}`;
  }
}

export class Client {
  constructor(config) {
    this.config = config;
  }

  async fetch(path) {
    const url = this.config.endpoint(path);
    return { url, status: 200 };
  }
}
