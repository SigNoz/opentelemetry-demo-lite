import unittest
from unittest.mock import Mock, patch

from workload_mode import is_eval_mode, select_recommendations, shipping_cost


class WorkloadModeTests(unittest.TestCase):
    def test_exact_opt_in(self):
        self.assertTrue(is_eval_mode({"EVAL_MODE": "1"}))
        for value in (None, "", "0", "true", "yes", " 1", "1 "):
            with self.subTest(value=value):
                self.assertFalse(is_eval_mode({"EVAL_MODE": value}))
        self.assertFalse(is_eval_mode({}))

    def test_defaults_read_environment_without_capturing_it_at_import(self):
        with patch.dict("os.environ", {"EVAL_MODE": "1"}):
            self.assertTrue(is_eval_mode())
            self.assertEqual(shipping_cost(2), (8.99, None))
            self.assertEqual(select_recommendations([1, 2, 3]), [1, 2, 3])
        with patch.dict("os.environ", {"EVAL_MODE": "0"}):
            self.assertFalse(is_eval_mode())

    def test_recommendations_take_first_five_without_randomness_or_mutation(self):
        available = [{"id": str(index)} for index in range(7)]
        rng = Mock()
        self.assertEqual(select_recommendations(available, True, rng), available[:5])
        self.assertEqual(len(available), 7)
        rng.sample.assert_not_called()

    def test_recommendations_handle_short_and_empty_catalogs(self):
        self.assertEqual(select_recommendations([], True), [])
        self.assertEqual(select_recommendations([{"id": "last"}], True), [{"id": "last"}])

    def test_normal_recommendations_keep_random_sampling(self):
        available = list(range(7))
        rng = Mock()
        rng.sample.return_value = [6, 4, 2, 0, 3]
        self.assertEqual(select_recommendations(available, False, rng), [6, 4, 2, 0, 3])
        rng.sample.assert_called_once_with(available, 5)
        rng.reset_mock()
        select_recommendations([], False, rng)
        rng.sample.assert_called_once_with([], 0)

    def test_shipping_is_fixed_per_item_without_randomness_or_handling_fees(self):
        rng = Mock()
        for items, expected in ((0, 5.99), (1, 7.49), (2, 8.99), (5, 13.49)):
            with self.subTest(items=items):
                self.assertEqual(shipping_cost(items, True, rng), (expected, None))
        rng.uniform.assert_not_called()
        rng.random.assert_not_called()

    def test_normal_shipping_keeps_variable_per_item_cost(self):
        rng = Mock()
        rng.uniform.return_value = 0.20
        rng.random.return_value = 0.2
        self.assertEqual(shipping_cost(2, False, rng), (9.39, None))
        rng.uniform.assert_called_once_with(-0.25, 0.25)

    def test_normal_shipping_keeps_random_handling_fee(self):
        rng = Mock()
        rng.uniform.side_effect = [-0.25, 1.25]
        rng.random.return_value = 0.1
        self.assertEqual(shipping_cost(2, False, rng), (9.74, 1.25))
        self.assertEqual(rng.uniform.call_args_list[1].args, (1.0, 3.0))


if __name__ == "__main__":
    unittest.main()
