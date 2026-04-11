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
NUM_FEATURES = 30
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

# Feature names must match Go feature constants in features.go.
# No hard-coded directory names — all relative or structural.
FEATURE_NAMES = [
    "case_exact", "case_match", "is_prefix", "is_suffix", "name_len_ratio",
    "type_func", "type_method", "type_struct", "type_iface", "type_other",
    "is_definition", "log_span",
    "depth", "depth_rank", "dir_popularity", "is_test_path", "name_in_path",
    "ext_majority_ratio",
    "span_rank", "max_span_ratio",
    "file_symbol_count",
    "name_has_underscore", "name_all_lower", "name_starts_upper", "name_is_short",
    "candidate_count", "query_len", "span_std_dev", "depth_std_dev", "ext_diversity",
]

TEST_PATTERNS = ["test/", "tests/", "testing/", "spec/", "__tests__/", "_test.", "test_"]

DEFINITION_TYPES = {"struct", "class", "interface", "type", "trait", "enum"}


def extract_features(query: str, candidate: dict) -> list[float]:
    """Extract per-candidate features matching Go's ExtractFeatures."""
    f = [0.0] * NUM_FEATURES
    name = candidate.get("name", "")
    sym_type = candidate.get("type", "")
    file = candidate.get("file", "")
    start = candidate.get("start_line", 0)
    end = candidate.get("end_line", 0)

    ql = query.lower()
    nl = name.lower()

    # Name match (0-4)
    f[0] = float(name == query)
    f[1] = float(nl == ql)
    f[2] = float(nl.startswith(ql))
    f[3] = float(nl.endswith(ql))
    f[4] = min(len(query) / max(len(name), 1), 1.0)

    # Type one-hot (5-9)
    type_map = {"function": 5, "method": 6, "struct": 7, "class": 7,
                "interface": 8, "trait": 8}
    idx = type_map.get(sym_type, 9)
    f[idx] = 1.0

    # Definition (10)
    f[10] = float(sym_type in DEFINITION_TYPES)

    # Span (11)
    span = max(end - start + 1, 1)
    f[11] = min(math.log2(span) / 10.0, 1.0)

    # Depth (12)
    parts = file.replace("\\", "/").split("/")
    f[12] = min(len(parts) - 1, 8) / 8.0

    # depth_rank (13) — set by extract_all_features
    # dir_popularity (14) — set by extract_all_features

    # Test path (15) — structural, not dir-name based
    fl = file.lower()
    f[15] = float(any(p in fl for p in TEST_PATTERNS))

    # Name in path (16)
    if ql and ql in fl:
        f[16] = 1.0

    # ext_majority_ratio (17) — set by extract_all_features
    # span_rank (18) — set by extract_all_features
    # max_span_ratio (19) — set by extract_all_features

    # File symbol count (20) — not available in training data, leave 0

    # Name character features (21-24)
    f[21] = float("_" in name)
    f[22] = float(name == name.lower())
    f[23] = float(len(name) > 0 and name[0].isupper())
    f[24] = float(len(name) <= 3)

    # Global context (25-29) — set by extract_all_features

    return f


def extract_all_features(query: str, candidates: list[dict]) -> list[list[float]]:
    """Extract features for all candidates, including cross-candidate features."""
    import os, math as _math
    from collections import Counter

    feats = [extract_features(query, c) for c in candidates]
    n = len(candidates)
    if n < 2:
        return feats

    # Extension majority ratio (17)
    exts = [os.path.splitext(c.get("file", ""))[1].lower() for c in candidates]
    ext_counts = Counter(exts)
    for i, ext in enumerate(exts):
        feats[i][17] = ext_counts[ext] / n

    # Span rank (18) + max span ratio (19)
    spans = [max(c.get("end_line", 0) - c.get("start_line", 0) + 1, 1) for c in candidates]
    max_span = max(spans) if spans else 1
    for i in range(n):
        smaller = sum(1 for s in spans if s < spans[i])
        feats[i][18] = smaller / (n - 1)
        feats[i][19] = spans[i] / max_span

    # Depth rank (13)
    depths = [c.get("file", "").replace("\\", "/").count("/") for c in candidates]
    for i in range(n):
        shallower = sum(1 for d in depths if d < depths[i])
        feats[i][13] = shallower / (n - 1)

    # Dir popularity (14)
    dirs = [c.get("file", "").replace("\\", "/").split("/")[0] for c in candidates]
    dir_counts = Counter(dirs)
    for i, d in enumerate(dirs):
        feats[i][14] = dir_counts[d] / n

    # Global context (25-29)
    cand_count = min(_math.log2(n) / 6.0, 1.0)
    query_len = min(len(query) / 20.0, 1.0)

    log_spans = [_math.log2(s) for s in spans]
    mean_ls = sum(log_spans) / n
    span_std = min(_math.sqrt(sum((ls - mean_ls) ** 2 for ls in log_spans) / n) / 3.0, 1.0)

    mean_d = sum(depths) / n
    depth_std = min(_math.sqrt(sum((d - mean_d) ** 2 for d in depths) / n) / 3.0, 1.0)

    ext_diversity = min(len(ext_counts) / 5.0, 1.0)

    for i in range(n):
        feats[i][25] = cand_count
        feats[i][26] = query_len
        feats[i][27] = span_std
        feats[i][28] = depth_std
        feats[i][29] = ext_diversity

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
            if not line:
                continue
            try:
                examples.append(json.loads(line))
            except json.JSONDecodeError:
                continue

    print(f"Loaded {len(examples)} examples")

    # Train/eval split: hold out entire repos for generalization measurement
    if args.eval_repos:
        eval_repos = set(args.eval_repos.split(","))
        train_examples = [ex for ex in examples if ex.get("repo", "") not in eval_repos]
        eval_examples = [ex for ex in examples if ex.get("repo", "") in eval_repos]
        print(f"  Train: {len(train_examples)} examples ({len(examples) - len(eval_examples)} repos)")
        print(f"  Eval:  {len(eval_examples)} examples (held-out repos: {', '.join(sorted(eval_repos))})")
    else:
        # Random 80/20 split as fallback
        import random
        random.seed(42)
        random.shuffle(examples)
        split = int(len(examples) * 0.8)
        train_examples = examples[:split]
        eval_examples = examples[split:]
        print(f"  Train: {len(train_examples)} examples (80%)")
        print(f"  Eval:  {len(eval_examples)} examples (20%)")

    dataset = RankingDataset(train_examples, max_candidates=args.max_candidates)
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

    # Eval on held-out data
    if eval_examples:
        model.eval()
        eval_dataset = RankingDataset(eval_examples, max_candidates=args.max_candidates)
        eval_loader = DataLoader(eval_dataset, batch_size=args.batch_size,
                                 shuffle=False, collate_fn=collate_fn)
        eval_correct = 0
        eval_total = 0
        with torch.no_grad():
            for features, labels, lengths in eval_loader:
                padding_mask = torch.arange(args.max_candidates).unsqueeze(0) >= lengths.unsqueeze(1)
                scores = model(features, key_padding_mask=padding_mask)
                scores = scores.masked_fill(padding_mask, float("-inf"))
                preds = scores.argmax(dim=1)
                eval_correct += (preds == labels).sum().item()
                eval_total += len(labels)
        eval_acc = eval_correct / max(eval_total, 1)
        print(f"\nEval accuracy: {eval_acc:.3f} ({eval_correct}/{eval_total})")
        print(f"Train accuracy: {acc:.3f}")
        print(f"Gap: {acc - eval_acc:.3f}")

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
    parser.add_argument("--eval-repos", default=None,
                        help="Comma-separated repo names to hold out for eval (e.g. 'kubernetes,react')")
    train(parser.parse_args())
