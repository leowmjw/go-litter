package musiccatalog

import (
	"context"
	"reflect"
	"testing"

	"app/internal/storage"
)

func TestMusicCatalogModule(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()

	moduleA, err := NewA(store, 4)
	if err != nil {
		t.Fatalf("new module A: %v", err)
	}

	if err := moduleA.Append(ctx, AlbumInput{Artist: "Post Malone", Name: "F-1 Trillion", Songs: []string{"Have The Heart ft. Dolly Parton"}}); err != nil {
		t.Fatalf("append album: %v", err)
	}
	if _, err := moduleA.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance A: %v", err)
	}

	moduleB, err := NewB(store, 4)
	if err != nil {
		t.Fatalf("new module B: %v", err)
	}

	count, err := moduleB.CountAlbums(ctx, "Post Malone")
	if err != nil || count != 1 {
		t.Fatalf("count = %d, %v", count, err)
	}

	album, found, err := moduleB.GetAlbum(ctx, "Post Malone", "F-1 Trillion")
	if err != nil || !found {
		t.Fatalf("get album: found=%v err=%v", found, err)
	}
	if album.Name != "F-1 Trillion" {
		t.Fatalf("album name = %q", album.Name)
	}
	if len(album.Songs) != 1 {
		t.Fatalf("songs = %d, want 1", len(album.Songs))
	}
	first := album.Songs[0]
	if first.Name != "Have The Heart" || !reflect.DeepEqual(first.FeaturedArtists, []string{"Dolly Parton"}) {
		t.Fatalf("first song = %+v", first)
	}

	if err := moduleB.Append(ctx, AlbumInput{Artist: "Frank Ocean", Name: "Channel Orange", Songs: []string{"White feat. John Mayer"}}); err != nil {
		t.Fatalf("append album B: %v", err)
	}
	if _, err := moduleB.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance B: %v", err)
	}

	album, found, err = moduleB.GetAlbum(ctx, "Frank Ocean", "Channel Orange")
	if err != nil || !found {
		t.Fatalf("get album B: found=%v err=%v", found, err)
	}
	if album.Name != "Channel Orange" || len(album.Songs) != 1 {
		t.Fatalf("album = %+v", album)
	}
	first = album.Songs[0]
	if first.Name != "White" || !reflect.DeepEqual(first.FeaturedArtists, []string{"John Mayer"}) {
		t.Fatalf("first song B = %+v", first)
	}
}

func TestAlbumsAreScopedByArtistWithinPartition(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := NewB(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []AlbumInput{
		{Artist: "Artist A", Name: "Greatest Hits", Songs: []string{"Song A"}},
		{Artist: "Artist B", Name: "Greatest Hits", Songs: []string{"Song B"}},
		{Artist: "Artist B", Name: "Second", Songs: []string{"Song C"}},
	} {
		if err := module.Append(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	albumA, found, err := module.GetAlbum(ctx, "Artist A", "Greatest Hits")
	if err != nil || !found || albumA.Songs[0].Name != "Song A" {
		t.Fatalf("artist A album = %+v, found=%v, err=%v", albumA, found, err)
	}
	albumB, found, err := module.GetAlbum(ctx, "Artist B", "Greatest Hits")
	if err != nil || !found || albumB.Songs[0].Name != "Song B" {
		t.Fatalf("artist B album = %+v, found=%v, err=%v", albumB, found, err)
	}
	countA, err := module.CountAlbums(ctx, "Artist A")
	if err != nil || countA != 1 {
		t.Fatalf("artist A count = %d, err=%v", countA, err)
	}
	countB, err := module.CountAlbums(ctx, "Artist B")
	if err != nil || countB != 2 {
		t.Fatalf("artist B count = %d, err=%v", countB, err)
	}
}
