package musiccatalog

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
)

const (
	ModuleName     storage.ModuleID   = "music-catalog"
	AlbumsDepot    storage.DepotID    = "albums"
	AlbumsState    storage.StateID    = "albums"
	AlbumsTopology storage.TopologyID = "albums"
)

var (
	ErrInvalidAlbum = errors.New("artist, name, and songs are required")
	songSplitRE     = regexp.MustCompile(`(?i)\s*(?:ft|feat)\.*`)
)

// Album is the stored, queryable album shape (songs parsed into Song records).
type Album struct {
	Name  string `json:"name"`
	Songs []Song `json:"songs"`
}

// AlbumInput is the raw depot record with string songs as received from clients.
type AlbumInput struct {
	Artist string   `json:"artist"`
	Name   string   `json:"name"`
	Songs  []string `json:"songs"`
}

type Song struct {
	Name            string   `json:"name"`
	FeaturedArtists []string `json:"featuredArtists"`
}

type albumV1 struct {
	Name  string   `json:"name"`
	Songs []string `json:"songs"`
}

type Module struct {
	Append      func(context.Context, AlbumInput) error
	Advance     func(context.Context) (int, error)
	AdvanceAll  func(context.Context) (int, error)
	Rebuild     func(context.Context) error
	GetAlbum    func(context.Context, string, string) (Album, bool, error)
	CountAlbums func(context.Context, string) (int, error)
}

func NewA(store *storage.Store, taskCount uint32) (*Module, error) {
	return newCatalog(store, taskCount, false)
}

func NewB(store *storage.Store, taskCount uint32) (*Module, error) {
	return newCatalog(store, taskCount, true)
}

func newCatalog(store *storage.Store, taskCount uint32, parse bool) (*Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	runtime, err := microbatch.New(ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: AlbumsTopology,
			Sources: []microbatch.Source{
				{
					Depot: AlbumsDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var a AlbumInput
						if err := json.Unmarshal(payload, &a); err != nil {
							return nil, err
						}
						return []byte(a.Artist), nil
					},
				},
			},
			Handle: func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
				for _, item := range items {
					var input AlbumInput
					if err := json.Unmarshal(item.Record.Payload, &input); err != nil {
						return err
					}
					if input.Artist == "" || input.Name == "" || len(input.Songs) == 0 {
						return ErrInvalidAlbum
					}
					var encoded []byte
					if parse {
						songs := make([]Song, len(input.Songs))
						for i, s := range input.Songs {
							songs[i] = parseSong(s)
						}
						encoded, err = json.Marshal(Album{Name: input.Name, Songs: songs})
					} else {
						encoded, err = json.Marshal(albumV1{Name: input.Name, Songs: input.Songs})
					}
					if err != nil {
						return err
					}
					state := storage.StatePartition{Module: ModuleName, State: AlbumsState, Partition: item.Partition}
					event.Set(state, albumKey(input.Artist, input.Name), encoded)
				}
				return nil
			},
		})
	if err != nil {
		return nil, err
	}

	module := &Module{}
	module.Append = func(ctx context.Context, a AlbumInput) error {
		if a.Artist == "" || a.Name == "" || len(a.Songs) == 0 {
			return ErrInvalidAlbum
		}
		payload, err := json.Marshal(a)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, AlbumsDepot, "", payload)
		return err
	}
	module.Advance = func(ctx context.Context) (int, error) { return runtime.Advance(ctx, AlbumsTopology) }
	module.AdvanceAll = func(ctx context.Context) (int, error) { return runtime.AdvanceAll(ctx) }
	module.Rebuild = runtime.Rebuild
	module.GetAlbum = func(ctx context.Context, artist, name string) (Album, bool, error) {
		state := storage.StatePartition{Module: ModuleName, State: AlbumsState, Partition: choosePartition([]byte(artist))}
		value, found, err := store.GetState(ctx, state, albumKey(artist, name))
		if err != nil || !found {
			return Album{}, found, err
		}
		var v2 Album
		if err := json.Unmarshal(value, &v2); err == nil {
			return v2, true, nil
		}
		var v1 albumV1
		if err := json.Unmarshal(value, &v1); err != nil {
			return Album{}, false, err
		}
		songs := make([]Song, len(v1.Songs))
		for i, s := range v1.Songs {
			songs[i] = parseSong(s)
		}
		return Album{Name: v1.Name, Songs: songs}, true, nil
	}
	module.CountAlbums = func(ctx context.Context, artist string) (int, error) {
		state := storage.StatePartition{Module: ModuleName, State: AlbumsState, Partition: choosePartition([]byte(artist))}
		prefix := append([]byte(artist), 0)
		end := append(append([]byte(nil), prefix...), 0xff)
		entries, err := store.ScanState(ctx, state, prefix, end, 1<<20, false)
		if err != nil {
			return 0, err
		}
		return len(entries), nil
	}
	return module, nil
}

func albumKey(artist, name string) []byte {
	key := append([]byte(artist), 0)
	return append(key, []byte(name)...)
}

func parseSong(song string) Song {
	parts := songSplitRE.Split(song, 2)
	name := strings.TrimSpace(parts[0])
	var featured []string
	if len(parts) > 1 {
		for _, a := range strings.Split(parts[1], ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				featured = append(featured, a)
			}
		}
	}
	return Song{Name: name, FeaturedArtists: featured}
}
