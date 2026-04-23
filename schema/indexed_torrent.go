package schema

import "time"

type IndexedTorrent struct {
	Title         string    `json:"title" xml:"title"`
	OriginalTitle string    `json:"original_title" xml:"original_title"`
	Details       string    `json:"details" xml:"link"`
	Year          string    `json:"year" xml:"year"`
	IMDB          string    `json:"imdb" xml:"imdb"`
	Category      int       `json:"category" xml:"category"`
	Audio         []Audio   `json:"audio" xml:"audio"`
	MagnetLink    string    `json:"magnet_link" xml:"enclosure"`
	Date          time.Time `json:"date" xml:"pubDate"`
	InfoHash      string    `json:"info_hash" xml:"info_hash"`
	Trackers      []string  `json:"trackers" xml:"trackers"`
	Size          string    `json:"size" xml:"size"`
	SizeInBytes   int64     `json:"size_bytes" xml:"size_bytes"`
	Files         []File    `json:"files,omitempty" xml:"files,omitempty"`
	LeechCount    int       `json:"leech_count" xml:"leech_count"`
	SeedCount     int       `json:"seed_count" xml:"seed_count"`
	Peers         int       `json:"peers" xml:"peers"`
	Seeders       int       `json:"seeders" xml:"seeders"`
	Similarity    float32   `json:"similarity" xml:"similarity"`
	Indexer       string    `json:"indexer" xml:"indexer"`
}

type File struct {
	Path string `json:"path"`
	Size string `json:"size"`
}
