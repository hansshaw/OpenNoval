package repository

import (
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CanvasTaskStopped(userID, id string, task any) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	var count int64
	err = db.Model(task).Where("user_id = ? AND id = ? AND status = ?", userID, id, "cancelled").Count(&count).Error
	return count > 0, err
}

func StopCanvasTask(userID, id, source string, task any, current time.Time) (bool, bool, error) {
	db, err := DB()
	if err != nil {
		return false, false, err
	}
	stopped, comfyCleanup := false, false
	err = db.Transaction(func(tx *gorm.DB) error {
		stamp := current.Format(time.RFC3339Nano)
		if video, ok := task.(*model.VideoTask); ok && video.ID != "" {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(video).Error; err != nil {
				return err
			}
		}
		update := tx.Model(task).
			Where("user_id = ? AND id = ? AND source = ? AND status IN ?", userID, id, source, []string{"queued", "processing", "running", "in_progress", "cancelled"}).
			Updates(map[string]any{"status": "cancelled", "updated_at": stamp, "completed_at": stamp})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			var count int64
			err := tx.Model(task).Where("user_id = ? AND id = ? AND source = ? AND status = ?", userID, id, source, "cancelled").Count(&count).Error
			stopped = count > 0
			return err
		}
		stopped = true
		if err := tx.Model(&model.ComfyBridgeRequest{}).
			Where("task_id = ? AND status IN ?", id, []string{"pending", "claimed"}).
			Updates(map[string]any{"status": "failed", "error": "用户停止", "updated_at": current, "completed_at": current}).Error; err != nil {
			return err
		}
		cleanup := tx.Model(&model.ComfyBridgeRequest{}).
			Where("task_id = ? AND status IN ? AND cleanup_ready_at IS NULL", id, []string{"failed", "succeeded"}).
			Updates(map[string]any{"cleanup_ready_at": current, "updated_at": current})
		comfyCleanup = cleanup.RowsAffected > 0
		return cleanup.Error
	})
	return stopped, comfyCleanup, err
}
