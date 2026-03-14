/**
 * Type definitions module.
 */

export class Config {
  constructor(host = "localhost", port = 8080) {
    this.host = host;
    this.port = port;
  }

  validate() {
    if (this.port <= 0) throw new Error("port must be positive");
    return true;
  }

  toJSON() {
    return { host: this.host, port: this.port };
  }
}

export class Logger {
  constructor(level = "info") {
    this.level = level;
  }

  log(msg) {
    console.log(`[${this.level}] ${msg}`);
  }
}
