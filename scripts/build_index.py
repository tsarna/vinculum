#!/usr/bin/env python3
"""
Build a FAISS vector index from Vinculum documentation for use with vinculum-ai.

Usage:
    python scripts/build_index.py [--docs-dir doc] [--output vcl-index]

The output directory contains two files produced by LangChain's FAISS.save_local():
    vcl-index/index.faiss   — binary vector index
    vcl-index/index.pkl     — pickled docstore (text + metadata)

Package for release:
    tar -czf vinculum-index.tar.gz -C vcl-index .

Users can then load it with:
    vinculum-ai --index ./vcl-index "question"

Or it will be auto-downloaded from the GitHub release by vinculum-ai's default mode.

Embedding model: BAAI/bge-small-en-v1.5 (fastembed, 384 dimensions, ~50 MB download).
This model must be used consistently — the index and all queries must use the same model.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


def main() -> None:
    p = argparse.ArgumentParser(
        description="Build a FAISS index from Vinculum .md documentation.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument(
        "--docs-dir",
        default="doc",
        metavar="DIR",
        help="Directory containing .md documentation files. (default: doc)",
    )
    p.add_argument(
        "--output",
        default="vcl-index",
        metavar="PATH",
        help="Output directory for the FAISS index files. (default: vcl-index)",
    )
    args = p.parse_args()

    # Import here so --help is instant
    from langchain_community.embeddings import FastEmbedEmbeddings
    from langchain_community.vectorstores import FAISS
    from langchain_core.documents import Document
    from langchain_text_splitters import RecursiveCharacterTextSplitter

    docs_dir = Path(args.docs_dir)
    if not docs_dir.is_dir():
        print(f"Error: docs directory not found: {docs_dir}", file=sys.stderr)
        sys.exit(1)

    md_files = sorted(docs_dir.rglob("*.md"))
    if not md_files:
        print(f"Error: no .md files found in {docs_dir}", file=sys.stderr)
        sys.exit(1)

    print(f"Loading {len(md_files)} .md files from {docs_dir}/")
    docs = [
        Document(
            page_content=path.read_text(encoding="utf-8"),
            metadata={"source": str(path.relative_to(docs_dir))},
        )
        for path in md_files
    ]

    splitter = RecursiveCharacterTextSplitter(chunk_size=800, chunk_overlap=100)
    chunks = splitter.split_documents(docs)
    print(f"Split into {len(chunks)} chunks")

    print("Loading embedding model (BAAI/bge-small-en-v1.5)…")
    embeddings = FastEmbedEmbeddings(model_name="BAAI/bge-small-en-v1.5")

    print("Embedding…")
    store = FAISS.from_documents(chunks, embeddings)

    Path(args.output).mkdir(parents=True, exist_ok=True)
    store.save_local(args.output)
    print(f"Index saved to {args.output}/  ({len(chunks)} chunks, 384-dim fastembed vectors)")


if __name__ == "__main__":
    main()
