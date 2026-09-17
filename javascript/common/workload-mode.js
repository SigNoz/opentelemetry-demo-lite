function isEvalMode(env = process.env) {
    return env.EVAL_MODE === '1';
}

function paymentDetails(evalMode = isEvalMode(), random = Math.random) {
    if (evalMode) {
        return { cardNumber: '4111111111111111', loyaltyLevel: 'silver', amount: '110.00', currency: 'USD' };
    }

    const cardNumber = `4${Math.floor(random() * 1e15).toString().padStart(15, '0')}`;
    const loyaltyLevel = ['bronze', 'silver', 'gold', 'platinum'][Math.floor(random() * 4)];
    const amount = (random() * 500 + 10).toFixed(2);
    const currency = ['USD', 'EUR', 'GBP', 'JPY'][Math.floor(random() * 4)];
    return { cardNumber, loyaltyLevel, amount, currency };
}

function fallbackUserId(evalMode = isEvalMode(), random = Math.random) {
    return evalMode ? 'user-1042' : `user-${Math.floor(random() * 10000)}`;
}

function shouldFail(probability, evalMode = isEvalMode(), random = Math.random) {
    return !evalMode && random() < probability;
}

function fallbackAds(allAds, evalMode = isEvalMode(), random = Math.random) {
    if (evalMode) return allAds.slice(0, 1);
    const count = Math.floor(random() * 3) + 1;
    return allAds.sort(() => 0.5 - random()).slice(0, count);
}

module.exports = { isEvalMode, paymentDetails, fallbackUserId, shouldFail, fallbackAds };
