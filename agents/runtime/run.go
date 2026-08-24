package runtime

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// CallBudget 是执行实例调用前扣减的次数账本。
type CallBudget struct {
	Remaining int64
}

// Consume 扣一次预算；耗尽返回 resource_exhausted。
func (b *CallBudget) Consume() error {
	if b == nil {
		return nil
	}
	if b.Remaining <= 0 {
		return fmt.Errorf("resource_exhausted")
	}
	b.Remaining--
	return nil
}

// Step 是执行实例内的一条命令对象。
type Step struct {
	Name               string
	Dangerous          bool
	Fail               bool
	Replay             ReplayPolicy
	CompensationReplay ReplayPolicy
	Budget             *CallBudget
	MemBytes           uint64
	Compensate         func(context.Context) error
	Run                func(context.Context) error
}

// RunRecord 记录逐步回执。
type RunRecord struct {
	Events []string
}

// Execute 按 Saga 执行：失败走补偿，取消走补偿。limits 可选，超量则 resource_exhausted。
func Execute(ctx context.Context, steps []Step, sandbox bool, rec *RunRecord, limits ...ResourceLimit) error {
	if rec == nil {
		rec = &RunRecord{}
	}
	var lim ResourceLimit
	if len(limits) > 0 {
		lim = limits[0]
	}
	done := 0
	for i, step := range steps {
		if step.Dangerous && !sandbox {
			rec.Events = append(rec.Events, "reject:"+step.Name)
			return fmt.Errorf("dangerous step %s: failed_precondition", step.Name)
		}
		if ExceedsLimit(step.MemBytes, lim.MemoryBytes) {
			rec.Events = append(rec.Events, "limit:"+step.Name)
			return fmt.Errorf("resource_exhausted")
		}
		if err := step.Budget.Consume(); err != nil {
			rec.Events = append(rec.Events, "budget:"+step.Name)
			return err
		}
		select {
		case <-ctx.Done():
			return compensate(ctx, steps[:done], rec, ctx.Err())
		default:
		}
		rec.Events = append(rec.Events, "start:"+step.Name)
		if step.Fail {
			rec.Events = append(rec.Events, "fail:"+step.Name)
			return compensate(ctx, steps[:done], rec, fmt.Errorf("step %s failed", step.Name))
		}
		if step.Run != nil {
			if err := step.Run(ctx); err != nil {
				rec.Events = append(rec.Events, "fail:"+step.Name)
				return compensate(ctx, steps[:done], rec, err)
			}
		}
		rec.Events = append(rec.Events, "ok:"+step.Name)
		done = i + 1
	}
	return nil
}

func compensate(ctx context.Context, done []Step, rec *RunRecord, cause error) error {
	for i := len(done) - 1; i >= 0; i-- {
		if done[i].Compensate == nil {
			continue
		}
		rec.Events = append(rec.Events, "compensate:"+done[i].Name)
		if err := done[i].Compensate(ctx); err != nil {
			return fmt.Errorf("%w: compensate %s: %v", cause, done[i].Name, err)
		}
	}
	return cause
}

// LimitProcess 给当前进程设置独立进程组，便于存活时限到期时强制终止整个进程树。
func LimitProcess() error {
	return limitCurrentProcess()
}

const (
	runMaxFiles      = 256
	runMaxMemory     = 256 << 20
	runMaxCPUSeconds = 60
	maxGoMemoryLimit = uint64(1<<63 - 1)
)

// LimitResources 设置 Go 运行时软内存上限、中央处理器时间与文件描述符上限；失败必须显式返回。
// 入参为零的字段用默认上限。当前平台不支持的资源限制错误会跳过，但不得把整个执行实例放行成无上限。
func LimitResources(limits ...ResourceLimit) error {
	lim := ResourceLimit{MemoryBytes: runMaxMemory, CPUSeconds: runMaxCPUSeconds, Files: runMaxFiles}
	if len(limits) > 0 {
		if limits[0].MemoryBytes > 0 {
			lim.MemoryBytes = limits[0].MemoryBytes
		}
		if limits[0].CPUSeconds > 0 {
			lim.CPUSeconds = limits[0].CPUSeconds
		}
		if limits[0].Files > 0 {
			lim.Files = limits[0].Files
		}
	}
	if lim.MemoryBytes > maxGoMemoryLimit {
		return fmt.Errorf("memory limit exceeds int64")
	}
	if err := LimitProcess(); err != nil {
		return err
	}
	if err := applyPlatformResourceLimits(lim); err != nil {
		return err
	}
	debug.SetMemoryLimit(int64(lim.MemoryBytes))
	return nil
}

// LimitsFromEnv 读取 Hatch 注入的资源上限；缺省字段为零，由 LimitResources 填默认值。
func LimitsFromEnv() ResourceLimit {
	return ResourceLimit{
		MemoryBytes: parseUintEnv(envMemoryLimit),
		CPUSeconds:  parseUintEnv(envRlimitCPU),
		Files:       parseUintEnv(envRlimitNOFILE),
	}
}

// SleepWithTTL 在存活时限到期时返回。
func SleepWithTTL(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	t := time.NewTimer(ttl)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return context.DeadlineExceeded
	}
}
