
// middleware/auth.js - Authentication middleware
const jwt = require('jsonwebtoken');
const User = require('../models/User');

const adminAuth = async (req, res, next) => {
    try {
        const token = req.header('Authorization')?.replace('Bearer ', '');
        if (!token) {
            throw new Error();
        }

        const decoded = jwt.verify(token, process.env.JWT_SECRET);
        const user = await User.findOne({
            _id: decoded.userId
        });

        if (!user) {
            throw new Error();
        }

        if (user.accountStatus !== 'active') {
            return res.status(403).json({
                success: false,
                message: `Account is ${user.accountStatus}. Please contact support.`
            });
        }

        // FIXED: Use array includes for better readability
        const allowedRoles = ['admin', 'merchant'];
        if (!allowedRoles.includes(user.role)) {
            return res.status(403).json({
                success: false,
                message: 'Access denied. Admin or merchant only.'
            });
        }

        req.token = token;
        req.user = user;
        next();
    } catch (error) {
        res.status(401).json({
            success: false,
            message: 'Please authenticate'
        });
    }
};

module.exports = adminAuth;