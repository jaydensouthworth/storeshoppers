package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"instore-shopper/internal/productimage"
)

const maxProductImages = 128
const maxProductImageBytes = 32 << 20

var ErrImageQuota = errors.New("The demo image library is full. Existing images and illustrations remain available; no image was removed.")
var ErrImageRate = errors.New("The shared demo has reached its image preview limit. Try again later or choose a bundled illustration.")

type ImageLibraryStats struct{ Count, Bytes int64 }

// ClaimImagePreview applies a shared, persistent limit before expensive decode.
// It contains no IP addresses, filenames or individual visitor identifiers.
func (s *Store) ClaimImagePreview() error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var minute, minuteCount, day, dayCount int64
	if err = tx.QueryRow(`SELECT minute_bucket,minute_count,day_bucket,day_count FROM product_image_limits WHERE id=1`).Scan(&minute, &minuteCount, &day, &dayCount); err != nil {
		return err
	}
	now := s.now().Unix()
	if minute != now/60 {
		minute, minuteCount = now/60, 0
	}
	if day != now/86400 {
		day, dayCount = now/86400, 0
	}
	if minuteCount >= 10 || dayCount >= 50 {
		return ErrImageRate
	}
	if _, err = tx.Exec(`UPDATE product_image_limits SET minute_bucket=?,minute_count=?,day_bucket=?,day_count=? WHERE id=1`, minute, minuteCount+1, day, dayCount+1); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ImageLibraryStats() (ImageLibraryStats, error) {
	var stats ImageLibraryStats
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(master)+length(thumbnail)),0) FROM product_images`).Scan(&stats.Count, &stats.Bytes)
	return stats, err
}

func validImageHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func imageAt(q querier, hash string) (productimage.Result, error) {
	var image productimage.Result
	if !validImageHash(hash) {
		return image, ErrNotFound
	}
	image.SHA256 = hash
	err := q.QueryRow(`SELECT normalization_version,mime,width,height,thumbnail_width,thumbnail_height,master,thumbnail FROM product_images WHERE hash=?`, hash).Scan(&image.Version, &image.Master.MIME, &image.Master.Width, &image.Master.Height, &image.Thumbnail.Width, &image.Thumbnail.Height, &image.Master.Data, &image.Thumbnail.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return image, ErrNotFound
	}
	image.Thumbnail.MIME = image.Master.MIME
	return image, err
}

func (s *Store) ProductImage(hash string) (productimage.Result, error) { return imageAt(s.db, hash) }

// AttachProductImage receives only a normalized server-held preview. Hashes,
// dimensions, output MIME and bytes are validated again at this boundary.
// The catalog version protects the association; price and stock do not change.
func (s *Store) AttachProductImage(id, version int64, key string, image productimage.Result) error {
	if err := productimage.ValidateResult(image); err != nil {
		return ErrInvalid
	}
	return s.changeProductImage(id, version, key, image.SHA256, &image)
}

func (s *Store) UseProductIllustration(id, version int64, key string) error {
	return s.changeProductImage(id, version, key, "", nil)
}

func (s *Store) changeProductImage(id, version int64, key, hash string, image *productimage.Result) error {
	if id < 1 || version < 1 || !validImageHash(key) || (hash != "" && !validImageHash(hash)) {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sum := sha256.Sum256([]byte(fmt.Sprintf("product-image:%d:%d:%s", id, version, hash)))
	commandHash := hex.EncodeToString(sum[:])
	var recorded string
	err = tx.QueryRow(`SELECT command_hash FROM product_image_commands WHERE command_key=?`, key).Scan(&recorded)
	if err == nil {
		if recorded != commandHash {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if p.Archived || p.CatalogVersion != version {
		return ErrConflict
	}
	if image != nil {
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM product_images WHERE hash=?`, hash).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			var count, size int64
			if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(master)+length(thumbnail)),0) FROM product_images`).Scan(&count, &size); err != nil {
				return err
			}
			if count >= maxProductImages || size+int64(len(image.Master.Data)+len(image.Thumbnail.Data)) > maxProductImageBytes {
				return ErrImageQuota
			}
			_, err = tx.Exec(`INSERT INTO product_images(hash,normalization_version,mime,width,height,thumbnail_width,thumbnail_height,master,thumbnail) VALUES(?,?,?,?,?,?,?,?,?)`, hash, image.Version, productimage.OutputMIME, image.Master.Width, image.Master.Height, image.Thumbnail.Width, image.Thumbnail.Height, image.Master.Data, image.Thumbnail.Data)
			if err != nil {
				return err
			}
		}
	}
	var association any
	if hash != "" {
		association = hash
	}
	if _, err = tx.Exec(`UPDATE products SET image_hash=?,catalog_version=catalog_version+1 WHERE id=? AND catalog_version=?`, association, id, version); err != nil {
		return err
	}
	details := "Use bundled illustration; retained uploaded artwork remains recoverable"
	if hash != "" {
		details = "Attach normalized demo image " + hash[:12]
	}
	if err = catalogEvent(tx, "product", id, "edit", p.Name, details); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO product_image_commands(command_key,command_hash) VALUES(?,?)`, key, commandHash); err != nil {
		return err
	}
	return tx.Commit()
}

// Image blobs and decode budgets survive a confirmed demo reset. Only current
// product associations reset. The recovery snapshot retains the old association.
// Never silently purge retained bytes or permit reset to replenish upload quota.
func retainProductImages(source, destination *sql.DB) error {
	rows, err := source.Query(`SELECT hash FROM product_images ORDER BY hash`)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var hash string
		if err = rows.Scan(&hash); err != nil {
			rows.Close()
			return err
		}
		hashes = append(hashes, hash)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(hashes) > maxProductImages {
		return ErrImageQuota
	}
	tx, err := destination.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total int
	for _, hash := range hashes {
		image, e := imageAt(source, hash)
		if e != nil {
			return e
		}
		if e = productimage.ValidateResult(image); e != nil {
			return fmt.Errorf("retained image integrity: %w", e)
		}
		total += len(image.Master.Data) + len(image.Thumbnail.Data)
		if total > maxProductImageBytes {
			return ErrImageQuota
		}
		var created string
		if e = source.QueryRow(`SELECT created FROM product_images WHERE hash=?`, hash).Scan(&created); e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT INTO product_images(hash,normalization_version,mime,width,height,thumbnail_width,thumbnail_height,master,thumbnail,created) VALUES(?,?,?,?,?,?,?,?,?,?)`, hash, image.Version, productimage.OutputMIME, image.Master.Width, image.Master.Height, image.Thumbnail.Width, image.Thumbnail.Height, image.Master.Data, image.Thumbnail.Data, created); e != nil {
			return e
		}
	}
	var minute, count, day, daily int64
	if err = source.QueryRow(`SELECT minute_bucket,minute_count,day_bucket,day_count FROM product_image_limits WHERE id=1`).Scan(&minute, &count, &day, &daily); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE product_image_limits SET minute_bucket=?,minute_count=?,day_bucket=?,day_count=? WHERE id=1`, minute, count, day, daily); err != nil {
		return err
	}
	return tx.Commit()
}
