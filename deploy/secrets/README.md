# 部署凭据目录

部署前在本目录创建下列只含单行值的文件，并把权限设置为 `0600`：

- `db_password`
- `traffic_db_password`
- `admin_password`
- `agent_bootstrap_token`
- `unit_bootstrap_token`
- `modelside_result_token`
- `central_worker_bootstrap_token`

除本说明外，本目录内容已被 Git 忽略。标准 Compose 只把这些文件以只读 secret 挂载给确实需要它们的进程，凭据不会进入容器命令行或普通环境变量。
