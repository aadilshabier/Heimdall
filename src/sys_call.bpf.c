//go:build ignore

#include "common.h"

/* --- Important Structure Definitions --- */
// Define rule to filter system calls with respective cgroup id
struct syscall_filter_key
{
	u32 syscall_nr;
	u64 cgroup_id;
};

// Empty placeholder value
struct filter_rule
{
	u8 pad;
};

struct process_info
{
	u32 pid;
	u32 uid;
	u32 syscall_nr;
	u64 cgroup_id;
	u8 comm[TASK_COMM_LEN];
};


/* --- BPF Map Definitions --- */
// Map for filtering system calls
struct
{
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1024);
	__type(key, struct syscall_filter_key);
	__type(value, struct filter_rule);
} filter_map SEC(".maps");

// Map for logging events
struct
{
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__type(value, struct process_info);
} syscall_events SEC(".maps");

// Map to store process info struct
struct
{
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, u32);
	__type(value, struct process_info);
	__uint(max_entries, 1);
} process_info_map SEC(".maps");

SEC("kprobe/x64_sys_call")
int sys_call_block(struct pt_regs *ctx)
{
	// Variables
	struct task_struct *task;
	pid_t pid;
	uid_t uid;
	u64 cgroup_id;
	unsigned int syscall_nr;
	struct filter_rule *rule;

	// System call number
	syscall_nr = PT_REGS_PARM2(ctx);

	// Getting PID, UID and CGroupID
	pid = bpf_get_current_pid_tgid() >> 32;
	uid = (uid_t)bpf_get_current_uid_gid();
	cgroup_id = bpf_get_current_cgroup_id();

	task = bpf_get_current_task_btf();

	// Command being executed
	char comm[TASK_COMM_LEN];
	bpf_get_current_comm(&comm, sizeof(comm));

	struct syscall_filter_key key = {
		.syscall_nr = syscall_nr,
		.cgroup_id = cgroup_id,
	};

	// Check if the system call matches cgroup id and kill process
	rule = bpf_map_lookup_elem(&filter_map, &key);
	if (rule)
	{
		long ret;
		ret = bpf_send_signal(SIGKILL);
		if (ret == 0)
		{
			bpf_printk("Blocking syscall %u for PID %d with UID %u and CgroupID %llu\n", syscall_nr, pid, uid, cgroup_id);

			const u32 key_zero = 0;
			struct process_info *info = bpf_map_lookup_elem(&process_info_map, &key_zero);
			if (info == NULL) return ret;
			
			info->pid = pid;
			info->uid = uid;
			info->syscall_nr = syscall_nr;
			info->cgroup_id = cgroup_id;
			bpf_get_current_comm(&info->comm, sizeof(info->comm));

			// Send event to userspace for logging
			bpf_perf_event_output(ctx, &syscall_events, BPF_F_CURRENT_CPU, info, sizeof(*info));
		}
	}

	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";