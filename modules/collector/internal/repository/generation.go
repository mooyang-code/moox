package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GetOrCreateGeneration(ctx context.Context, db *gorm.DB, key string, generation time.Time) (domain.MarketGeneration, error) {
	if key == "" || generation.IsZero() {
		return domain.MarketGeneration{}, fmt.Errorf("generation key and timestamp are required")
	}
	var result domain.MarketGeneration
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.MarketGeneration
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("c_generation_key=?", key).Take(&current).Error
		if err == nil {
			result = current
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		current = domain.MarketGeneration{GenerationKey: key, Epoch: 1, Generation: generation.UTC(), Status: "active", ModifyTime: time.Now().UTC()}
		if err := tx.Create(&current).Error; err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func AdvanceGeneration(ctx context.Context, db *gorm.DB, key string, generation time.Time) (domain.MarketGeneration, error) {
	if key == "" || generation.IsZero() {
		return domain.MarketGeneration{}, fmt.Errorf("generation key and timestamp are required")
	}
	var result domain.MarketGeneration
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.MarketGeneration
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("c_generation_key=?", key).Take(&current).Error
		if err == gorm.ErrRecordNotFound {
			current = domain.MarketGeneration{GenerationKey: key, Epoch: 1, Generation: generation.UTC(), Status: "active"}
		} else if err != nil {
			return err
		} else {
			if !generation.After(current.Generation) {
				result = current
				return nil
			}
			current.Epoch++
			current.Generation = generation.UTC()
		}
		current.ModifyTime = time.Now().UTC()
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}
