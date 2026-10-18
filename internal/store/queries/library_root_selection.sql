-- name: ListConfiguredLibraryRootSelections :many
SELECT id,name,media_type,health,generation,baseline_completed FROM library_root ORDER BY name,id;
