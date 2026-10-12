package database

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertPendingTask inserts or overwrites a journal row by ID. Attempts is
// deliberately not overwritten: a recovery re-record refreshes the payload
// but must not reset the infrastructure-failure counter.
func UpsertPendingTask(ctx context.Context, task PendingTask) error {
	return db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"user_id", "payload", "created_at"}),
		}).
		Create(&task).Error
}

func DeletePendingTask(ctx context.Context, id string) error {
	return db.WithContext(ctx).Where("id = ?", id).Delete(&PendingTask{}).Error
}

func ListPendingTasks(ctx context.Context) ([]PendingTask, error) {
	var tasks []PendingTask
	err := db.WithContext(ctx).Order("created_at").Find(&tasks).Error
	return tasks, err
}

func BumpPendingTaskAttempts(ctx context.Context, id string) error {
	return db.WithContext(ctx).Model(&PendingTask{}).Where("id = ?", id).
		UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error
}

// CloseDB releases the database handle (used by tests and graceful shutdown).
func CloseDB() error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
