#!/usr/bin/env python3
"""Train the tiny transformer ranking model.

Usage:
    python train.py labeled.jsonl --output weights.bin

Input format (one JSON object per line):
    {"query": "probe", "candidates": [...], "label": 0}

    candidates: list of objects with fields matching CandidateFeatures
    label: index of the correct candidate (0-based)

Output: binary file of float32 weights matching Go's Weights struct layout.
"""

import argparse
import json
import math
import struct
import sys

import torch
import torch.nn as nn
import torch.nn.functional as F
from torch.utils.data import Dataset, DataLoader

# Must match Go constants
NUM_FEATURES = 34
DIM = 16
NUM_HEADS = 2
HEAD_DIM = DIM // NUM_HEADS
FFN_HIDDEN = 48
NUM_LAYERS = 2


class RankingTransformer(nn.Module):
    """Tiny transformer for candidate ranking."""

    def __init__(self):
        super().__init__()
        self.proj = nn.Linear(NUM_FEATURES, DIM)
        self.layers = nn.ModuleList([
            TransformerLayer() for _ in range(NUM_LAYERS)
        ])
        self.score_head = nn.Linear(DIM, 1)

    def forward(self, x, key_padding_mask=None):
        """x: [batch, num_candidates, NUM_FEATURES] → [batch, num_candidates]
        key_padding_mask: [batch, num_candidates] True = padded (ignore)
        """
        x = self.proj(x)
        for layer in self.layers:
            x = layer(x, key_padding_mask)
        return self.score_head(x).squeeze(-1)


class TransformerLayer(nn.Module):
    def __init__(self):
        super().__init__()
        self.attn = nn.MultiheadAttention(DIM, NUM_HEADS, batch_first=True)
        self.ln1 = nn.LayerNorm(DIM)
        self.ffn = nn.Sequential(
            nn.Linear(DIM, FFN_HIDDEN),
            nn.GELU(),
            nn.Linear(FFN_HIDDEN, DIM),
        )
        self.ln2 = nn.LayerNorm(DIM)

    def forward(self, x, key_padding_mask=None):
        attn_out, _ = self.attn(x, x, x, key_padding_mask=key_padding_mask)
        x = self.ln1(x + attn_out)
        x = self.ln2(x + self.ffn(x))
        return x


# --- Feature extraction (must match Go's ExtractFeatures) ---

FEATURE_NAMES = [
    "case_exact", "case_match", "is_prefix", "is_suffix", "name_len_ratio",
    "type_func", "type_method", "type_struct", "type_iface", "type_other",
    "is_definition", "log_span",
    "depth", "is_include", "is_core", "is_peripheral", "is_tools",
    "is_test", "is_vendor", "is_doc", "is_sample", "is_scripts",
    "ext_c", "ext_go", "ext_rust", "ext_py_ts",
    "ext_majority_ratio", "span_rank", "file_symbol_count",
    "name_has_underscore", "name_all_lower", "name_starts_upper", "name_is_short",
]

CORE_DIRS = {"kernel", "core", "init", "mm", "fs", "net", "block", "ipc",
             "security", "internal", "pkg", "cmd", "src", "lib"}
PERIPHERAL_DIRS = {"drivers", "plugins", "extensions", "addons", "contrib",
                   "adapters", "connectors", "integrations"}
TOOLS_DIRS = {"tools", "tool", "util", "utils", "hack", "misc"}
VENDOR_DIRS = {"vendor", "node_modules", "third_party"}
DOC_DIRS = {"docs", "doc", "documentation", "Documentation"}
SAMPLE_DIRS = {"examples", "example", "samples", "sample", "demo", "demos"}
SCRIPT_DIRS = {"scripts", "script", "build", "ci", "deploy"}

C_EXTS = {".c", ".h", ".cc", ".cpp", ".hpp", ".cxx"}
PY_TS_EXTS = {".py", ".ts", ".js", ".tsx", ".jsx"}

DEFINITION_TYPES = {"struct", "class", "interface", "type", "trait", "enum"}


def extract_features(query: str, candidate: dict) -> list[float]:
    """Extract feature vector matching Go's ExtractFeatures."""
    f = [0.0] * NUM_FEATURES
    name = candidate.get("name", "")
    sym_type = candidate.get("type", "")
    file = candidate.get("file", "")
    start = candidate.get("start_line", 0)
    end = candidate.get("end_line", 0)

    ql = query.lower()
    nl = name.lower()

    # Name match
    f[0] = float(name == query)
    f[1] = float(nl == ql)
    f[2] = float(nl.startswith(ql))
    f[3] = float(nl.endswith(ql))
    f[4] = min(len(query) / max(len(name), 1), 1.0)

    # Type one-hot
    type_map = {"function": 5, "method": 6, "struct": 7, "class": 7,
                "interface": 8, "trait": 8}
    idx = type_map.get(sym_type, 9)
    f[idx] = 1.0

    # Definition
    f[10] = float(sym_type in DEFINITION_TYPES)

    # Span
    span = max(end - start + 1, 1)
    f[11] = min(math.log2(span) / 10.0, 1.0)

    # Path
    import os
    parts = file.replace("\\", "/").split("/")
    f[12] = min(len(parts) - 1, 8) / 8.0  # depth
    top = parts[0] if parts else ""

    f[13] = float(top == "include")
    f[14] = float(top in CORE_DIRS)
    f[15] = float(top in PERIPHERAL_DIRS)
    f[16] = float(top in TOOLS_DIRS)

    fl = file.lower()
    f[17] = float(any(s in fl for s in ["test/", "tests/", "testing/", "spec/",
                                         "__tests__/", "_test.", "test_"]))
    f[18] = float(top in VENDOR_DIRS)
    f[19] = float(top in DOC_DIRS)
    f[20] = float(top in SAMPLE_DIRS)
    f[21] = float(top in SCRIPT_DIRS)

    # Extension
    _, ext = os.path.splitext(file)
    ext = ext.lower()
    if ext in C_EXTS:
        f[22] = 1.0
    elif ext == ".go":
        f[23] = 1.0
    elif ext == ".rs":
        f[24] = 1.0
    elif ext in PY_TS_EXTS:
        f[25] = 1.0

    # Cross-candidate features (26-28) are set by extract_all_features below
    # f[26] = ext_majority_ratio (set later)
    # f[27] = span_rank (set later)

    # File symbol count (28) — not available in training data, leave 0

    # Name character features
    f[29] = float("_" in name)
    f[30] = float(name == name.lower())
    f[31] = float(len(name) > 0 and name[0].isupper())
    f[32] = float(len(name) <= 3)
    # f[33] unused (padding to 34)

    return f


def extract_all_features(query: str, candidates: list[dict]) -> list[list[float]]:
    """Extract features for all candidates, including cross-candidate features."""
    import os
    feats = [extract_features(query, c) for c in candidates]
    n = len(candidates)
    if n < 2:
        return feats

    # Extension majority ratio
    exts = [os.path.splitext(c.get("file", ""))[1].lower() for c in candidates]
    from collections import Counter
    ext_counts = Counter(exts)
    for i, ext in enumerate(exts):
        feats[i][26] = ext_counts[ext] / n

    # Span rank
    spans = [max(c.get("end_line", 0) - c.get("start_line", 0) + 1, 1) for c in candidates]
    for i in range(n):
        smaller = sum(1 for s in spans if s < spans[i])
        feats[i][27] = smaller / (n - 1)

    return feats


# --- Dataset ---

class RankingDataset(Dataset):
    def __init__(self, examples, max_candidates=50):
        self.examples = []
        self.max_candidates = max_candidates
        for ex in examples:
            cands = ex["candidates"][:max_candidates]
            label = ex.get("label", 0)
            # Skip if too few candidates or label is out of bounds after truncation
            if len(cands) >= 2 and 0 <= label < len(cands):
                self.examples.append(ex)

    def __len__(self):
        return len(self.examples)

    def __getitem__(self, idx):
        ex = self.examples[idx]
        query = ex["query"]
        candidates = ex["candidates"][:self.max_candidates]
        label = ex["label"]

        features = torch.tensor(
            extract_all_features(query, candidates),
            dtype=torch.float32,
        )
        n = len(candidates)
        # Pad to max_candidates
        if n < self.max_candidates:
            pad = torch.zeros(self.max_candidates - n, NUM_FEATURES)
            features = torch.cat([features, pad])

        return features, label, n


def collate_fn(batch):
    features = torch.stack([b[0] for b in batch])
    labels = torch.tensor([b[1] for b in batch], dtype=torch.long)
    lengths = torch.tensor([b[2] for b in batch], dtype=torch.long)
    return features, labels, lengths


# --- Weight export ---

def export_weights(model: RankingTransformer, path: str):
    """Export weights in the binary format matching Go's Weights struct."""
    sd = model.state_dict()

    def write_tensor(f, key, transpose=False):
        t = sd[key].detach().cpu().float()
        if transpose and t.dim() == 2:
            t = t.t().contiguous()  # [outDim, inDim] → [inDim, outDim] for Go
        f.write(struct.pack(f"<{t.numel()}f", *t.flatten().tolist()))

    def write_scalar(f, key):
        v = sd[key].detach().cpu().float().item()
        f.write(struct.pack("<f", v))

    with open(path, "wb") as f:
        # Feature projection: PyTorch [DIM, NUM_FEATURES] → Go [NUM_FEATURES, DIM]
        write_tensor(f, "proj.weight", transpose=True)
        write_tensor(f, "proj.bias")

        for l in range(NUM_LAYERS):
            prefix = f"layers.{l}"
            # PyTorch MHA packs Q,K,V into one in_proj_weight [3*DIM, DIM]
            in_w = sd[f"{prefix}.attn.in_proj_weight"]  # [3*DIM, DIM]
            in_b = sd[f"{prefix}.attn.in_proj_bias"]    # [3*DIM]
            q_w, k_w, v_w = in_w.chunk(3, dim=0)
            q_b, k_b, v_b = in_b.chunk(3, dim=0)

            # Each Q/K/V weight is [DIM, DIM] → transpose to [DIM, DIM] for Go
            for w, b in [(q_w, q_b), (k_w, k_b), (v_w, v_b)]:
                wt = w.t().contiguous()
                f.write(struct.pack(f"<{wt.numel()}f", *wt.flatten().tolist()))
                f.write(struct.pack(f"<{b.numel()}f", *b.flatten().tolist()))

            # Output projection
            write_tensor(f, f"{prefix}.attn.out_proj.weight", transpose=True)
            write_tensor(f, f"{prefix}.attn.out_proj.bias")

            # LayerNorm 1 (no transpose — gamma/beta are 1D)
            write_tensor(f, f"{prefix}.ln1.weight")
            write_tensor(f, f"{prefix}.ln1.bias")

            # FFN
            write_tensor(f, f"{prefix}.ffn.0.weight", transpose=True)
            write_tensor(f, f"{prefix}.ffn.0.bias")
            write_tensor(f, f"{prefix}.ffn.2.weight", transpose=True)
            write_tensor(f, f"{prefix}.ffn.2.bias")

            # LayerNorm 2
            write_tensor(f, f"{prefix}.ln2.weight")
            write_tensor(f, f"{prefix}.ln2.bias")

        # Score head: [1, DIM] → [DIM] (flatten, no transpose needed for 1D)
        write_tensor(f, "score_head.weight")
        write_scalar(f, "score_head.bias")

    total = sum(p.numel() for p in model.parameters())
    print(f"Exported {total} parameters ({total * 4} bytes) to {path}")


# --- Training ---

def train(args):
    # Load data
    examples = []
    with open(args.input) as f:
        for line in f:
            line = line.strip()
            if line:
                examples.append(json.loads(line))

    print(f"Loaded {len(examples)} examples")

    dataset = RankingDataset(examples, max_candidates=args.max_candidates)
    loader = DataLoader(dataset, batch_size=args.batch_size, shuffle=True,
                        collate_fn=collate_fn)

    model = RankingTransformer()
    total_params = sum(p.numel() for p in model.parameters())
    print(f"Model: {total_params} parameters")

    optimizer = torch.optim.AdamW(model.parameters(), lr=args.lr, weight_decay=0.01)
    scheduler = torch.optim.lr_scheduler.CosineAnnealingLR(optimizer, args.epochs)

    for epoch in range(args.epochs):
        model.train()
        total_loss = 0
        correct = 0
        total = 0

        for features, labels, lengths in loader:
            # key_padding_mask: True = padded position (ignore in attention)
            padding_mask = torch.arange(args.max_candidates).unsqueeze(0) >= lengths.unsqueeze(1)
            scores = model(features, key_padding_mask=padding_mask)

            # Mask padded positions for loss
            mask = ~padding_mask
            scores = scores.masked_fill(~mask, float("-inf"))

            loss = F.cross_entropy(scores, labels)
            optimizer.zero_grad()
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            optimizer.step()

            total_loss += loss.item()
            preds = scores.argmax(dim=1)
            correct += (preds == labels).sum().item()
            total += len(labels)

        scheduler.step()
        acc = correct / max(total, 1)
        avg_loss = total_loss / max(len(loader), 1)
        print(f"Epoch {epoch+1}/{args.epochs}: loss={avg_loss:.4f} acc={acc:.3f}")

    # Export
    model.eval()
    export_weights(model, args.output)
    print(f"Saved weights to {args.output}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Train ranking transformer")
    parser.add_argument("input", help="Labeled examples (JSONL)")
    parser.add_argument("--output", "-o", default="rank_model.bin",
                        help="Output weights file")
    parser.add_argument("--epochs", type=int, default=100)
    parser.add_argument("--batch-size", type=int, default=32)
    parser.add_argument("--lr", type=float, default=1e-3)
    parser.add_argument("--max-candidates", type=int, default=50)
    train(parser.parse_args())
