"""Tests for StorageObject dataclass and StorageClient ABC."""

import pytest

from techai_webutils.core.interfaces.storage import StorageClient, StorageObject


class TestStorageObject:
    """Tests for the storage object."""

    def test_construction(self) -> None:
        """Test that StorageObject preserves the metadata returned from object listings.

        **Why this test is important:**
          - StorageObject is the metadata record callers read when enumerating documents in S3;
            key drives later download/delete calls, size and content_type drive UI and parsing
            decisions, and etag underpins integrity/caching, so any field being dropped or
            swapped would misidentify or mishandle a stored document

        **What it tests:**
          - key, size, content_type, and etag are all stored and returned unchanged from
            construction
        """
        obj = StorageObject(
            key="docs/file.pdf",
            size=1024,
            content_type="application/pdf",
            last_modified="2024-01-01T00:00:00Z",
            etag="abc123",
        )
        assert obj.key == "docs/file.pdf"
        assert obj.size == 1024
        assert obj.content_type == "application/pdf"
        assert obj.etag == "abc123"


class TestStorageClient:
    """Tests for the storage client."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that the StorageClient ABC cannot be instantiated without its methods.

        **Why this test is important:**
          - StorageClient is the S3-compatible object-storage contract the document pipeline
            depends on; an instantiable stub would accept upload() and silently discard the
            binary, or return empty bytes from download(), losing user documents without error
          - Forces every storage backend to implement the full upload/download/delete/exists/
            presign_url/list_objects surface before it can be constructed

        **What it tests:**
          - Instantiating StorageClient directly raises TypeError because its object-storage
            methods are abstract
        """
        with pytest.raises(TypeError):
            StorageClient()  # type: ignore[abstract]
