package checkin

import (
	"context"
	"fmt"

	"lsm/internal/entity"
	"lsm/internal/store"
)

// Resource is something issued at reception: radios, stop signs …
type Resource struct {
	ID          entity.ID
	Name        string
	Description string
	// Tracked resources have numbered instances (radios on a rack);
	// untracked ones are simply issued or not (stop signs).
	Tracked bool
}

// ResourceInstance is one registered numbered item of a tracked resource.
// Issue records may also carry unregistered numbers: a radio entry is
// never blocked.
type ResourceInstance struct {
	ID         entity.ID
	ResourceID entity.ID
	Number     string
	Retired    bool
}

// InsertResource stores a new resource.
func InsertResource(ctx context.Context, q store.DBTX, r Resource) error {
	_, err := q.ExecContext(ctx, `INSERT INTO resources (id, name, description, tracked) VALUES (?, ?, ?, ?)`,
		r.ID, r.Name, r.Description, r.Tracked)
	if err != nil {
		return fmt.Errorf("checkin: insert resource %q: %w", r.Name, err)
	}
	return nil
}

// ListResources returns every resource by name.
func ListResources(ctx context.Context, q store.DBTX) ([]Resource, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, description, tracked FROM resources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Resource
	for rows.Next() {
		var r Resource
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Tracked); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertResourceInstance registers a numbered instance.
func InsertResourceInstance(ctx context.Context, q store.DBTX, in ResourceInstance) error {
	_, err := q.ExecContext(ctx, `INSERT INTO resource_instances (id, resource_id, number, retired)
		VALUES (?, ?, ?, ?)`, in.ID, in.ResourceID, in.Number, in.Retired)
	if err != nil {
		return fmt.Errorf("checkin: insert resource instance %s: %w", in.Number, err)
	}
	return nil
}

// ResourceInstances returns a resource's registered instances.
func ResourceInstances(ctx context.Context, q store.DBTX, resourceID entity.ID) ([]ResourceInstance, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, resource_id, number, retired FROM resource_instances
		WHERE resource_id = ? ORDER BY length(number), number`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResourceInstance
	for rows.Next() {
		var in ResourceInstance
		if err := rows.Scan(&in.ID, &in.ResourceID, &in.Number, &in.Retired); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
