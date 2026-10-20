package app

import "net/http"

func storageLow(available, total uint64, threshold int) bool {
	return total > 0 && float64(available)/float64(total)*100 < float64(threshold)
}
func (s *Server) storageStatus(w http.ResponseWriter, _ *http.Request, _ *User) error {
	s.siteMu.RLock()
	threshold := s.site.StorageWarningPercent
	s.siteMu.RUnlock()
	path := s.Config.StorageRoot
	if path == "" {
		path = s.Config.DataDir
	}
	available, total, e := diskSpace(path)
	if e != nil || total == 0 {
		writeJSON(w, 200, map[string]any{"available": false, "threshold_percent": threshold})
		return nil
	}
	writeJSON(w, 200, map[string]any{"available": true, "available_bytes": available, "total_bytes": total, "available_percent": float64(available) / float64(total) * 100, "threshold_percent": threshold, "low_space": storageLow(available, total, threshold)})
	return nil
}
