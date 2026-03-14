/**
 * Entry point using aliased imports from both modules.
 */

import { Config as TypeConfig, Logger } from "./types";
import { Config as ApiConfig, Client } from "./api";

export function createApp() {
  const serverConfig = new TypeConfig("0.0.0.0", 9090);
  const apiConfig = new ApiConfig("/api", "v2", 10000);
  const logger = new Logger("debug");

  serverConfig.validate();
  apiConfig.validate();

  logger.log(`Server: ${serverConfig.host}:${serverConfig.port}`);
  logger.log(`API: ${apiConfig.endpoint("health")}`);

  return { serverConfig, apiConfig, logger };
}

export function createClient() {
  const config = new ApiConfig();
  return new Client(config);
}

export function quickSetup() {
  const config = new TypeConfig();
  return config.toJSON();
}
