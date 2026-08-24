package runtime

import (
	"context"
)

// WatchSupervisor 监视只读存活管道；监督进程消失时立即终止当前进程组。
func WatchSupervisor(fd int) error {
	return watchSupervisorPlatform(fd)
}

// WatchCancellation 把监督进程的持久取消通知转换为执行上下文取消。
// Unix 调用成功后由监视器接管 fd；调用方不得再用另一 os.File 包装同一描述符。
func WatchCancellation(fd int, cancel context.CancelFunc) error {
	return watchCancellationPlatform(fd, cancel)
}
