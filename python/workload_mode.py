"""Request choices shared by the normal demo and its finite workload."""
import os
import random


def is_eval_mode(environ=None):
    return (os.environ if environ is None else environ).get("EVAL_MODE") == "1"


def select_recommendations(available, eval_mode=None, rng=random):
    enabled = is_eval_mode() if eval_mode is None else eval_mode
    if enabled:
        return available[:5]
    return rng.sample(available, min(5, len(available)))


def shipping_cost(num_items, eval_mode=None, rng=random):
    enabled = is_eval_mode() if eval_mode is None else eval_mode
    per_item_cost = 1.50 if enabled else 1.50 + rng.uniform(-0.25, 0.25)
    total_cost = 5.99 + num_items * per_item_cost
    handling_fee = None
    if not enabled and rng.random() < 0.2:
        handling_fee = rng.uniform(1.0, 3.0)
        total_cost += handling_fee
    return round(total_cost, 2), handling_fee
