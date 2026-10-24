package download

import "github.com/dalamudx/PulseMedia/internal/store/postgres/db"

func sameWatchObservationVersion(snapshot, current db.DownloadTask) bool {
	return snapshot.State == current.State && snapshot.UpdatedAt.Valid && current.UpdatedAt.Valid &&
		snapshot.UpdatedAt.InfinityModifier == 0 && current.UpdatedAt.InfinityModifier == 0 &&
		snapshot.UpdatedAt.Time.Equal(current.UpdatedAt.Time)
}
