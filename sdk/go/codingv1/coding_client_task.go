package codingv1

import (
	"context"
	"strconv"
)

func (c *Client) ListTasks(ctx context.Context, s ProjectScope) (*TaskList, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[TaskList](c.projectRequest(ctx, s), "GET", base+"/tasks", nil)
}

func (c *Client) GetTask(ctx context.Context, s ProjectScope, id string) (*TaskDetail, error) {
	path, err := taskPath(s, id)
	if err != nil {
		return nil, err
	}
	return jsonCall[TaskDetail](c.projectRequest(ctx, s), "GET", path, nil)
}

func (c *Client) AssociateTask(ctx context.Context, s ProjectScope, req *AssociateTaskRequest) (*Task, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[Task](c.projectRequest(ctx, s), "POST", base+"/tasks", req)
}

func (c *Client) CreateTask(ctx context.Context, s ProjectScope, key string, req *CreateTaskRequest) (*Task, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[Task](mutation(c.projectRequest(ctx, s), key), "POST", base+"/tasks/new", req)
}

func (c *Client) ActOnTask(ctx context.Context, s ProjectScope, id string, req *TaskActionRequest) (*Task, error) {
	path, err := taskPath(s, id)
	if err != nil {
		return nil, err
	}
	return jsonCall[Task](c.projectRequest(ctx, s), "POST", path+"/actions", req)
}

func (c *Client) SyncTask(ctx context.Context, s ProjectScope, id string) (*Task, error) {
	path, err := taskPath(s, id)
	if err != nil {
		return nil, err
	}
	return jsonCall[Task](c.projectRequest(ctx, s), "POST", path+"/sync", nil)
}

func (c *Client) GetTaskDiff(ctx context.Context, s ProjectScope, id string) (*TaskDiff, error) {
	path, err := taskPath(s, id)
	if err != nil {
		return nil, err
	}
	return jsonCallLimit[TaskDiff](c.projectRequest(ctx, s), "GET", path+"/diff", nil, 32<<20)
}

func (c *Client) GetRunTrace(
	ctx context.Context,
	s ProjectScope,
	task, run string,
	after int64,
) (*RuntimeTrace, error) {
	path, err := taskPath(s, task)
	if err != nil {
		return nil, err
	}
	return jsonCallLimit[RuntimeTrace](
		c.projectRequest(ctx, s),
		"GET",
		path+"/runs/"+escape(run)+"/trace?after="+strconv.FormatInt(after, 10),
		nil,
		32<<20,
	)
}
