// 模板服务器管理 API 的响应类型，字段与 internal/templateserver 的 JSON 一致。
export interface TemplateView {
  id: number
  key: string
  name: string
  item: string
  tag: string
  scene: string
  kind: string
  sha256: string
  icon_sha256: string
  image_base64: string
  enabled: boolean
  updated_at: string
}

export interface ClusterView {
  cluster_key: string
  sha256: string
  phash: string
  scene: string
  item: string
  tag: string
  status: string
  occurrences: number
  client: string
  image_base64: string
  first_seen_at: string
  last_seen_at: string
}

export interface ReleaseView {
  Revision: number
  CreatedAt: string
  Note: string
  ManifestSHA256: string
  Signature: string
  Deployed: boolean
  WorkerURL: string
}

export interface StatusView {
  revision: number
  public_base: string
  worker_name: string
  cf_deploy: boolean
  enabled: number
  pending_clusters: number
}
